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
go test -race ./...          # unit tests
golangci-lint run            # lint
go run ./cmd/api             # public on :8080, admin on :9090
curl localhost:9090/metrics  # Prometheus metrics
docker build -t apartment-finder .
```

Configuration comes from environment variables: `ADDR` (default `:8080`), `ADMIN_ADDR`
(default `:9090`) and `LOG_LEVEL` (`debug`, `info`, `warn` or `error`; default `info`).

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
