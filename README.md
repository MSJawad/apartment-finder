# apartment-finder

Scores apartment listings by what matters day to day: walking and cycling time to the
places you go most, nearby amenities, rent and size. Each listing gets a 0-100 score with
a breakdown, shown on a map and in a ranked table with adjustable weights.

It started as a Google Sheets + Apps Script tool I used while apartment hunting in Erfurt.
This is the rebuild as a deployed service: Go, PostgreSQL + PostGIS, a self-hosted OSRM
router, and k3s on Oracle Cloud's free tier.

## Status

Work in progress. v1 is done when a stranger can open the URL, add a listing, and see it
scored on the map.

## Running locally

Requires Go 1.27 and Docker.

```sh
go test -race ./...          # all tests; integration tests start a PostGIS container
go test -short ./...         # unit tests only, no Docker needed
golangci-lint run            # lint

# A local database, built from the same Dockerfile as production
docker build -t apartment-finder-postgis deploy/postgis
docker run -d --name apartment-finder-db -e POSTGRES_PASSWORD=dev -p 5432:5432 apartment-finder-postgis
export DATABASE_URL='postgres://postgres:dev@localhost:5432/postgres?sslmode=disable'

go run ./cmd/api             # public on :8080, admin on :9090
go run ./cmd/worker          # admin on :9090, so run one at a time or set ADMIN_ADDR
curl localhost:9090/metrics  # Prometheus metrics
docker build -t apartment-finder .
```

Configuration comes from environment variables: `DATABASE_URL` (required), `ADDR`
(default `:8080`), `ADMIN_ADDR` (default `:9090`) and `LOG_LEVEL` (`debug`, `info`, `warn`
or `error`; default `info`).

## Decisions

Each non-obvious choice, with the options considered and why this one won.

### Probes and metrics on a separate admin port

The api serves public traffic on `:8080` and `/healthz`, `/readyz` and `/metrics` on
`:9090`. Only `:8080` is routed through the ingress, so metrics can't leak to the
internet, even if an ingress rule is too broad. The alternative, one port with an
ingress rule that blocks `/metrics`, fails open: one mistake in the rule exposes it.

The trade-off: a probe on the admin port doesn't go through the public server. Both
servers run in one process, and if either fails, the process exits and Kubernetes
restarts it, so a dead public server can't go unnoticed. Admin requests aren't logged,
because probes and scrapes every few seconds would bury real traffic.

### Metric labels use route patterns, not raw paths

`http_requests_total` is labelled with the matched route (`/api/listings/{id}`), not the
URL (`/api/listings/42`). Every distinct label value is a new time series in
Prometheus, so labelling by raw path would let anyone scanning random URLs grow memory
without limit. Unknown HTTP methods collapse to `other` for the same reason.

### One image, two binaries

The api and worker ship in one image; the worker's Kubernetes Deployment overrides the
entrypoint. Both always deploy from the same commit, and CI builds and pushes one image
instead of two. The cost is a few MB of unused binary in each container.

### Cross-compiling for arm64

The production VM is arm64 and GitHub's standard runners are amd64. The options:
emulate arm64 with QEMU (much slower, since every build step runs under emulation),
build on an arm64 runner, or cross-compile. Go cross-compiles natively, so the
Dockerfile's build stage runs on the build machine's own CPU and only the final stage,
which just copies files, targets arm64. Any runner or laptop builds the arm64 image at
native speed.

### distroless/static, running as non-root

A static Go binary needs only CA certificates and timezone data. `distroless/static`
has those and nothing else: no shell, no package manager. That means fewer CVEs to
patch, and nothing for an attacker to work with if the process were compromised. It
runs as a non-root user. The cost is that you can't `kubectl exec` into a shell to
debug; ephemeral debug containers cover that.

### Everything pinned by digest

Base images are pinned by `@sha256` digest, and GitHub Actions by commit SHA, with the
readable version in a comment. Tags can be moved to point at different content; a
digest can't. So a build today and a build next month use the same inputs, and a
compromised upstream tag can't slip into CI.

### A job queue in Postgres, not Kafka or Redis

Adding a listing kicks off slow work: geocoding, amenity lookups and routing, each
calling an external service. That work runs in the background, in a queue stored in a
Postgres table.

Kafka or Redis would mean another stateful system to deploy, monitor, back up and fit
into a 12 GB VM, for a workload of maybe a few hundred jobs a day. Postgres handles that
easily. More importantly, a job inserted in the same transaction as its listing exists
exactly when the listing does. With a separate broker, the two writes can disagree: the
listing commits but publishing the job fails, or the job is published for a listing
that then rolls back. Fixing that needs an outbox table, which is a Postgres queue
anyway.

The trade-off is throughput: a table-based queue tops out far below Kafka, and every
poll is a query. If this ever needed thousands of jobs a second, a broker would earn its
place.

### Claiming jobs: SKIP LOCKED, a lease and a fencing token

A worker claims a job with `SELECT ... FOR UPDATE SKIP LOCKED` inside an `UPDATE`.
`SKIP LOCKED` lets many workers poll the same table at once: each skips rows another
worker is claiming instead of waiting for them.

The claim is a lease (`locked_until`), not a transaction held open while the job runs.
Jobs call external APIs and can take seconds. A transaction open that long ties up a
database connection and holds back vacuum, Postgres' cleanup of old row versions. With
a lease, the claim commits immediately; if the worker dies, the lease runs out and
another worker retries the job.

Leases bring a classic hazard: a worker that stalls past its lease, then wakes up and
reports a result for a job someone else now holds. Every claim therefore gets a new
random `lease_token`, and completing or failing a job requires the current token. A
stale worker's update matches no row and is discarded.

Delivery is at least once: a job whose lease runs out mid-run can run twice. Handlers
are written to be safe to repeat, for example by upserting results.

Failed jobs retry with exponential backoff (30s, 1m, 2m, up to an hour), randomly
shortened by up to half so jobs that failed together don't retry in lockstep. After 8
attempts, about an hour of retries, a job is marked `dead` with its last error.
Attempts are counted at claim time, so a job that keeps crashing its worker also runs
out of attempts instead of crashing workers forever.

### Polling instead of LISTEN/NOTIFY

Idle workers check for jobs once a second. Postgres' `LISTEN/NOTIFY` could wake them
instantly, but a notification sent while no worker is listening (during a deploy, say)
is lost, so polling would still be needed as a fallback. One cheap indexed query a
second costs nothing, and a second of latency is invisible next to geocoding and
routing.

### Our own PostGIS image

The official `postgis/postgis` image is published for amd64 only, and the VM is arm64. A
third-party multi-arch build exists, but its Debian variants stopped being updated in
early 2026, so they were missing months of Postgres security releases.
`deploy/postgis/Dockerfile` uses the same recipe as the official image instead: the
official `postgres:18` image, which is multi-arch and promptly patched, plus PostGIS
from the PostgreSQL project's own apt repository. Both are pinned, so builds are
repeatable, and security updates are an explicit version bump. The integration tests
build and run this same image, so tests and production can't drift apart.

### Migrations: a small runner instead of a library

Migrations are numbered SQL files embedded in the binary. On startup, the api and worker
both run them, holding a Postgres advisory lock so that pods starting at the same time
take turns. Each migration runs in one transaction together with the row recording it,
so a failed migration leaves nothing behind. A checksum per migration catches anyone
editing a migration that has already run, which would leave old and new databases
silently different.

Libraries such as goose add down-migrations and a CLI. Migrations here only go forward:
undoing a change is a new migration, which is how changes reach production anyway. That
fits in one short file with no dependency.

### Integration tests: one container, one database per test

Tests start a single PostGIS container per test binary and migrate a template database
once. Each test gets its own database copied from the template, which takes
milliseconds. Tests run in parallel without seeing each other's rows, and no test has to
clean up after itself.

### Scores use fixed thresholds, not a ranking

Each criterion maps its value onto 0 to 1 between two thresholds: a supermarket within
5 minutes' walk scores 1, one 20 minutes away scores 0, and times in between scale
linearly. The listing's score is the weighted average of those, times 100.

The alternative is scoring relative to the other listings, where the cheapest gets full
marks. That feels like a ranking, but adding one listing then changes every other
listing's score. And since each visitor has their own list, the same apartment would
score differently for different people. With fixed thresholds, a listing's score depends
only on the listing and the weights.

A criterion that couldn't be measured, say because no route was found, is left out, and
the other weights are scaled up to fill its share. The breakdown marks it, so a high
score with half the criteria missing doesn't pass for a complete one. "There's no gym
nearby" is different: it's a measurement and scores 0.

Every line of the scoring package is covered by tests, and CI enforces it.
