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

Requires Go 1.27, Docker and [osmium](https://osmcode.org/osmium-tool/) (for the amenity
import and its tests).

```sh
go test -race ./...          # all tests; integration tests start a PostGIS container
go test -short ./...         # unit tests only, no Docker needed
golangci-lint run            # lint

# Download the region's OpenStreetMap extract into data/ and build the walking and
# cycling routing data from it (about 2 minutes and 2 GB of memory)
scripts/prepare-routing.sh

# The database, built from the same Dockerfile as production, and the two routers
docker compose up -d --wait
export DATABASE_URL='postgres://postgres:dev@localhost:5432/postgres?sslmode=disable'
export REGION_CONFIG=config/thuringia.json
curl 'localhost:5001/route/v1/foot/11.0357,50.9774;11.0377,50.9728'  # walk (bike is on :5002)

# Load the everyday places (supermarkets, stops, parks...) from the same extract
go run ./cmd/import-amenities data/osm/region.osm.pbf

go run ./cmd/api             # public on :8080, admin on :9090
go run ./cmd/worker          # admin on :9090, so run one at a time or set ADMIN_ADDR
curl localhost:9090/metrics  # Prometheus metrics
docker build -t apartment-finder .
```

Configuration comes from environment variables: `DATABASE_URL` and `REGION_CONFIG`
(required), `ADDR`
(default `:8080`), `ADMIN_ADDR` (default `:9090`) and `LOG_LEVEL` (`debug`, `info`, `warn`
or `error`; default `info`).

## API

JSON over HTTP. A visitor is identified by a cookie that the first `POST` sets; see
"Visitors are a cookie, not accounts" below.

| Request | What it does |
| --- | --- |
| `GET /api/region` | The region's name, bounding box and where the map opens |
| `GET /api/listings` | The example listings and your own, ranked, with each score's breakdown and the criteria |
| `GET /api/listings?weight.<criterion>=<0-10>` | The same, with your weights, e.g. `weight.rent_per_m2=5&weight.category:gym=0` |
| `POST /api/listings` | Add a listing: `address`, `warm_rent_eur`, `size_m2`, `rooms`, and optionally `source_url`. Answers `202 Accepted` with the listing, still `pending` |
| `GET /api/listings/{id}` | One listing, with the same `weight.` parameters |
| `DELETE /api/listings/{id}` | Delete one of your listings |
| `GET /api/places` | Your personal places, such as work, each with the `criterion` key it scores under |
| `POST /api/places` | Add a place: `name`, `address`, `mode` (`walk` or `bike`). Answers `202 Accepted` |
| `DELETE /api/places/{id}` | Delete one of your places |

Errors are `{"error": "...", "fields": {"rooms": "..."}}`, with `fields` naming each
invalid field. Status codes: 400 for invalid input, 403 for a request from another site,
404 for a listing or place that isn't yours or doesn't exist, 409 for an eleventh place,
413 and 415 for oversized or non-JSON bodies, 429 with `Retry-After` when a limit is
reached.

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

### Scores are worked out on every read

The worker stores measurements, such as "6.5 minutes' walk to Netto", never scores. Each
`GET /api/listings` scores the listings from those measurements, with the weights in the
query string.

- **Sliders are cheap.** Moving a weight is just another read: nothing is re-measured or
  written.
- **One scoring implementation.** The scoring logic lives only in Go; the page shows what
  the API returns and has no scoring code of its own to drift out of step.
- **Thresholds can change.** New thresholds in the region config apply to every listing at
  the next read.

It costs a little CPU per request: ranking 50 listings on 10 criteria takes about 0.1 ms.

### Visitors are a cookie, not accounts

There are no sign-ups. A visitor is a random id in a cookie, and whoever holds the cookie
sees and edits that visitor's listings. Accounts would mean passwords or email
addresses: personal data, and a lot to secure, for a site where people try out a few
listings.

- **The server picks the ids.** A cookie with an id the server never handed out is
  replaced with a fresh one. Ids are version 4 UUIDs: 122 random bits, too many to guess
  someone else's.
- **Reading creates nothing.** A visitor is created by their first listing, so crawlers
  and people just looking leave no rows behind.
- **The cookie can't be read by scripts** (`HttpOnly`), and is only sent over HTTPS in
  production (`Secure`, set whenever the request came through the TLS ingress).

### Cross-site request forgery

Because the cookie is the credential, another site could try to make a visitor's browser
add or delete listings with it. Three things stop that:
- **Go's `http.CrossOriginProtection`** rejects `POST` and `DELETE` requests that browsers
  mark as coming from another site (the `Sec-Fetch-Site` and `Origin` headers).
- **`SameSite=Lax` on the cookie**: browsers don't send it with requests from other
  sites, except for plain link clicks.
- **JSON-only bodies**: an HTML form on another site can't send `application/json`.

### Limits count additions, under one lock

Each listing or place costs a request to Photon, a free public geocoder, plus routing
work on our small server. So additions are capped:
- **Per visitor:** 10 an hour and 30 a day.
- **Site-wide:** 300 a day. Clearing cookies gets around the per-visitor limits, but not
  this one, so Photon never sees more than 300 new addresses a day from us.

How the limits are counted:
- **In Postgres, not in memory.** Counts stay right with any number of API replicas and
  across restarts.
- **Additions, not listings.** Each one is logged in a `submissions` table, so deleting a
  listing doesn't give its slot back.
- **Rolling windows.** "A day" is the last 24 hours, which needs no time zone and has no
  rush at midnight.
- **One lock.** Counting and recording happen under a transaction-scoped advisory lock.
  Without it, two simultaneous requests could both see 9 of 10 used and both go through.
  One lock for everyone is fine at a few hundred additions a day. A test fires 10
  simultaneous additions at a limit of 3 and expects exactly 3 to succeed.

A refused addition gets `429 Too Many Requests` with a `Retry-After` header saying when
the oldest counted addition leaves its window.

### Personal places score the trip from each listing

A visitor can add up to 10 places of their own, such as work or a university, each
reached on foot or by bike. Each place becomes one more criterion, for that visitor only:
the trip there from each listing, scoring 1 at 10 minutes or less and 0 at 45 or more.
They count for the example listings too, so a new visitor sees how the examples suit
them.

- **One direction: from the listing.** Trips are measured from home to the place, the
  morning commute, never the other way. On a bike the two can differ because of one-way
  streets: from Anger 1 to the Hauptbahnhof takes 5.8 minutes, back 5.5.
- **Two jobs, no gaps.** A new listing measures the trips to the places that see it, and
  a new place measures the trips from the listings its visitor sees. If both are added at
  the same moment, each job might run before the other has a location. So each job
  commits its own location before looking for the other side: whichever reads second is
  guaranteed to see the first, and the trip is always measured.
- **Upserts, not delete-and-insert.** Both jobs can measure the same trip at the same
  time, so trips are written with `INSERT ... ON CONFLICT DO UPDATE`, and the second write
  simply wins.
- **A place costs what a listing costs.** Adding one geocodes an address, so places count
  towards the same limits as listings.

### Listings come in through a Source

The spec rules out scraping listing sites such as ImmoScout24 or Kleinanzeigen, which their
terms forbid. Listings are typed in by hand, through a `Source` interface: a source turns
a submission into a checked draft, and each stored listing records which source it came
from. The add-listing form is the only one. A new input that is allowed, say a partner API,
would be a new `Source`, with nothing downstream changing.

The optional link to the original advert is stored and shown, never fetched. Only
`http` and `https` links are accepted, so a link can't run script when clicked.

### The region lives in config, not code

`config/thuringia.json` holds everything specific to a place:
- the covered area and where the map opens
- the language addresses are shown in
- the OpenStreetMap extract to route on
- the thresholds and default weights for rent, size and rooms
- the everyday categories (supermarket, transit stop, pharmacy, doctor, park, train
  station, gym), each with its OSM tags, travel mode, thresholds and weight

Covering another city means writing another file. The file is validated strictly at
startup:
- unknown fields are errors, so a typo can't be silently ignored
- each threshold must point the right way, so a config with "best" and "worst" swapped
  fails instead of quietly inverting a score

Search radii aren't configured. They follow from each category's worst time: if a
20-minute walk already scores 0, there's no point looking further than 20 minutes' walk in
a straight line.

### Every upstream response is cached, misses included

Photon and other public OpenStreetMap services run on donated capacity and ask clients to
be gentle. Every response is stored in Postgres, keyed by the request URL:
- answers are kept for 90 days, since addresses rarely move
- "nothing found" answers are kept too, for a shorter time, so a typo isn't sent again
  every time a job retries
- errors such as 429 or 502 are never cached; the job fails and the queue retries it later
  with backoff

Requests to each service are spaced out by a rate limiter and carry a User-Agent naming
this app, as their usage policies ask. The limiter runs within one process, and the
worker runs as a single replica, so that limit holds for the whole deployment.

### Geocoding only accepts house or street matches, and shows what it matched

Photon is asked only for buildings and streets, never districts or cities. Otherwise
"Erfurt" would quietly place the listing at the city center and score that. German
listings often leave out the house number, so street-level matches are accepted.

That isn't enough on its own: a bare "Erfurt" still matches a building, the main
station. So each listing stores and shows the address it was matched to, and whether it
matched a house or only a street. A wrong match is visible to the user instead of
silently scored. Matches outside the region count as not found, because there's no
routing data there.

### Amenities come from our own import, not the Overpass API

The original plan was to look up nearby amenities with the public Overpass API, the
standard way to query OpenStreetMap. Testing it changed the plan:
- **The public server pushed back.** It answered "at capacity", and its error page says
  the public endpoint is meant for browser-based, human-operated tools, and that
  automated use should run its own instance.
- **The wiki caps automated use.** The OpenStreetMap wiki asks regular automated users to
  stay around 1/100 of the one-off limits, about 100 queries a day.

A backend worker calling it for every new listing doesn't fit either.

So the places are imported instead:
- **Same source data.** The worker already needs the region's OpenStreetMap extract for
  routing. The extract is filtered down to the seven categories and loaded into an
  `amenities` table in PostGIS, where a weekly job refreshes it.
- **One indexed query.** "The five nearest supermarkets" becomes a single query: the GiST
  index answers `ORDER BY geom <-> point` (nearest-neighbour search) without scanning the
  table.
- **Areas measure from their edge.** A park is stored as its polygon, so distance counts
  from its nearest edge rather than its middle, and routes go to that edge.

The trade-offs:
- **Data age.** The data is as old as the last import, at most a week, which is fine for
  supermarkets and parks.
- **Upkeep.** Keeping the import running is now our job.
- **Safety.** A refresh swaps the whole table in one transaction, and an empty import is
  refused rather than wiping every amenity.

The import uses osmium, the standard command-line tool for OpenStreetMap extracts:
- **Filtering:** `tags-filter` keeps only objects carrying a category's tags. The filter
  expressions are generated from the region config, which stays the single source of
  truth.
- **Exporting:** `export` turns the result into GeoJSON, assembling parks drawn as
  multipolygon relations into proper polygons.
- **Areas, not outlines:** osmium exports a closed way twice, as its outline and as the
  area inside it. Only points and polygons are kept, so a park is stored as an area and a
  listing inside it is distance 0 from it.

The tests run the real osmium on a small hand-made extract, then load the result into
PostGIS and query it.

### Routing: our own OSRM, built from the same extract

Travel times come from [OSRM](https://project-osrm.org/), run as two routers: one built
with OSRM's walking profile, one with its cycling profile.

**Why not a public router?** The public OSRM servers, the ones openstreetmap.org's
directions use, allow "reasonable, non-commercial use", at most one request per second
and "no heavy usage", with no uptime guarantee. A worker that scores listings around
the clock shouldn't depend on that. Running our own also means there's no rate limit to
work around, and travel times can't change under us when someone else updates their
setup.

**The data is built, not shipped.** `scripts/prepare-routing.sh` downloads the region's
extract, checks it against Geofabrik's checksum, and runs OSRM's three preparation
steps for each profile. The result, about 1.3 GB for both profiles, lives in a data
directory rather than an image, so the router image stays the pinned upstream one and
fresh map data never needs a new image.

**MLD, not CH.** OSRM can prepare data for two algorithms:
- **Contraction Hierarchies (CH)** answer queries fastest, but take much longer to
  prepare.
- **Multi-Level Dijkstra (MLD)** prepares in under a minute here and answers a query in
  a few milliseconds.

The worker sends one table request per listing and travel mode, so query speed doesn't
matter at this scale, and quick preparation makes refreshing the data cheap.

**Measured costs** for Thuringia (a 153 MB extract), per profile:

| Step | Peak memory | Time, 8 threads |
| --- | --- | --- |
| `osrm-extract` | 1.9 GiB | 40 s (110 s with 2 threads) |
| `osrm-partition` | 0.7 GiB | 20 s |
| `osrm-customize` | 0.7 GiB | 4 s |
| Running router | 0.4 GiB | - |

Preparation peaks at about 0.85 GB of disk per profile and leaves about 0.65 GB.
These numbers set the deployment's memory limits.

**Checked against openstreetmap.org.** The same trips, routed on openstreetmap.org's
directions with OSRM, came out within 20% of ours:

| Trip | openstreetmap.org | Ours |
| --- | --- | --- |
| Anger 1 to Hauptbahnhof, walking | 707 m, 9 min | 666 m, 8.1 min |
| Anger 1 to Hauptbahnhof, cycling | 1.0 km, 7 min | 1.0 km, 5.8 min |
| University to Hauptbahnhof, walking | 3.2 km, 42 min | 3.1 km, 37.1 min |
| University to Hauptbahnhof, cycling | 3.8 km, 17 min | 3.2 km, 16.7 min |

Ours run a little shorter. The public servers use their own walking and cycling
profiles, which differ from OSRM's defaults.
