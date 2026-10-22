# apartment-finder

Scores apartment listings by what matters day to day: walking and cycling time to the
places you go most, nearby amenities, rent and size. Each listing gets a 0-100 score with
a breakdown, shown on a map and in a ranked table with adjustable weights.

It started as a Google Sheets + Apps Script tool I used while apartment hunting in Erfurt.
This is the rebuild as a deployed service: Go, PostgreSQL + PostGIS, a self-hosted OSRM
router, and k3s on Oracle Cloud's free tier.

## Status

v1 is live at **https://92-5-186-10.sslip.io**: add a listing and it's scored on the map
within seconds. The address is the server's IP, so it would change if the server were
ever rebuilt.

## Architecture

What runs, all on one free Arm server. Arrows point from the caller:

```mermaid
flowchart TB
    browser(["Browser"])

    subgraph server["Oracle Cloud Arm server, running k3s"]
        traefik["Traefik<br/>HTTPS via<br/>Let's Encrypt"]
        api["API, 2 pods<br/>page and JSON"]
        worker["Worker, 1 pod<br/>runs the jobs"]
        postgres[("PostgreSQL<br/>+ PostGIS<br/>listings, jobs<br/>places, cache")]
        osrm["OSRM routers<br/>walk and bike"]
        importer["Places import<br/>every week"]
    end

    subgraph public["Public services built on OpenStreetMap"]
        photon["Photon<br/>geocoder"]
        geofabrik["Geofabrik<br/>map extracts"]
    end

    browser -->|HTTPS| traefik --> api
    api -->|"reads, queues jobs"| postgres
    worker -->|"claims jobs, stores results"| postgres
    worker -->|"travel times"| osrm
    worker -->|"geocodes addresses"| photon
    importer -->|"everyday places"| postgres
    importer -->|"downloads the extract"| geofabrik
    osrm -->|"downloads the extract<br/>on first start"| geofabrik
```

Adding a listing: the API stores it, with a job to enrich it, in one transaction and
answers at once. The worker claims the job, geocodes the address with Photon, finds the
nearest supermarket, stop, park and so on in PostGIS, and asks the routers how long the
walk or ride to each takes. It stores those measurements, never a score: each read
scores the listings from the measurements and the visitor's weights. The page polls
until the listing is ready, which takes about a second.

How a change ships:

```mermaid
flowchart TB
    merge(["Merge to main"]) --> actions["GitHub Actions<br/>test, build<br/>for arm64"]
    actions -->|publishes| ghcr["GitHub's registry<br/>images and<br/>chart 0.1.N"]
    ghcr -->|"pulled by"| flux["Flux<br/>in the cluster"]
    flux -->|"upgrades, or<br/>rolls back a failure"| app["The app"]
```

CI's last step waits until the site serves the new commit. CI never holds credentials
for the server or the cloud: the cluster pulls its releases, and Terraform runs only
from a laptop.

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
go run ./cmd/import-amenities data/osm/region.osm.pbf  # without an argument, it downloads the extract itself

go run ./cmd/api             # public on :8080, admin on :9090
OSRM_WALK_URL=http://localhost:5001 OSRM_BIKE_URL=http://localhost:5002 ADMIN_ADDR=:9091 \
  go run ./cmd/worker        # admin on :9091, beside the api's
curl -X POST localhost:8080/api/listings -H 'Content-Type: application/json' \
  -d '{"address": "Anger 50, Erfurt", "warm_rent_eur": 820, "size_m2": 61, "rooms": 2}'
curl localhost:8080/api/listings  # scored within a few seconds
open http://localhost:8080        # the page
curl localhost:9090/metrics  # Prometheus metrics
docker build -t apartment-finder .
```

Configuration comes from environment variables:

| Variable | Default | |
| --- | --- | --- |
| `DATABASE_URL` | required | PostgreSQL connection string |
| `REGION_CONFIG` | required | the region file, e.g. `config/thuringia.json` |
| `OSRM_WALK_URL`, `OSRM_BIKE_URL` | required by the worker | the routers, `http://localhost:5001` and `:5002` with compose |
| `PHOTON_URL` | `https://photon.komoot.io` | the geocoder |
| `ADDR` | `:8080` | the API's public address |
| `ADMIN_ADDR` | `:9090` | probes and metrics |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

## Deploying the server

Everything in the cloud is created by Terraform in `deploy/terraform`: a virtual network,
a firewall, and one Arm server with k3s. It runs from your laptop only; CI never holds
cloud credentials.

You need an Oracle Cloud account with Frankfurt as its home region, an API key in
`~/.oci/config` (Oracle's console shows the file's contents when you add the key), and
Terraform.

```sh
cd deploy/terraform
cp terraform.tfvars.example terraform.tfvars   # fill in tenancy_ocid from ~/.oci/config
terraform init
terraform apply                                # about a minute; prints the server's IP
ssh ubuntu@<ip> cloud-init status --wait       # first boot installs k3s, about a minute

# kubectl from the laptop, through SSH: the Kubernetes API isn't reachable from outside
ssh ubuntu@<ip> sudo cat /etc/rancher/k3s/k3s.yaml \
  | sed 's#127.0.0.1:6443#127.0.0.1:16443#' > ~/.kube/apartment-finder.yaml
chmod 600 ~/.kube/apartment-finder.yaml
ssh -fN -L 16443:127.0.0.1:6443 ubuntu@<ip>
KUBECONFIG=~/.kube/apartment-finder.yaml kubectl get nodes

terraform destroy                              # removes everything
```

On "Out of host capacity", free Arm servers are short in that availability domain: set
`availability_domain = 2` (or 3) in `terraform.tfvars` and apply again.

## Deploying the app

The cluster deploys the app itself: Flux, running inside it, watches this repo and the
container registry. Merging to `main` is the whole deploy.
1. CI tests the commit, builds the three arm64 images, and publishes the Helm chart in
   `deploy/chart` to GHCR as version `0.1.<run number>`, with the commit as its app
   version.
2. Within a minute, Flux sees the higher chart version and upgrades the release.
3. Helm replaces the pods, each old one only after its replacement is ready. A release
   that isn't ready within 10 minutes is rolled back.
4. CI's last job polls `https://<host>/api/version` until it reports the commit, and fails
   after 15 minutes if it never does.

Setting up a new server, once, with `kubectl` working as above:

```sh
# The database password, generated in the cluster and stored nowhere else. First, so the
# app finds it when Flux installs it.
kubectl create namespace apartment-finder
kubectl -n apartment-finder create secret generic apartment-finder-db \
  --from-literal=password="$(openssl rand -hex 24)"

# Flux installs itself, commits its own manifests to deploy/flux/clusters/production,
# and gives the cluster a read-only deploy key for this repo
GITHUB_TOKEN=$(gh auth token) flux bootstrap github --owner=MSJawad \
  --repository=apartment-finder --branch=main --path=deploy/flux/clusters/production \
  --personal --read-write-key=false
```

A new server also has a new IP. Put it, with dashes, in `host` in
`deploy/flux/apps/apartment-finder.yaml`, and in the `SITE_URL` variable CI checks
(`gh variable set SITE_URL --body https://<ip-with-dashes>.sslip.io`).

The first install takes about 8 minutes: each router downloads the OpenStreetMap extract
and builds its routing data, and the worker imports the everyday places before it starts.
Later deploys take a minute or two.

Day to day:

```sh
flux get helmreleases -A                         # what's deployed, and whether it worked
kubectl -n apartment-finder get pods
kubectl -n apartment-finder logs deploy/apartment-finder-api
kubectl -n apartment-finder port-forward svc/apartment-finder-api-admin 9090
                                                 # then http://localhost:9090/metrics
```

## API

JSON over HTTP. A visitor is identified by a cookie that the first `POST` sets; see
"Visitors are a cookie, not accounts" below.

| Request | What it does |
| --- | --- |
| `GET /api/region` | The region's name, its outline and the box around it, and where the map opens |
| `GET /api/listings` | The example listings and your own, ranked, with each score's breakdown and the criteria. The examples come from the region config, on streets without house numbers, so none points at a real home |
| `GET /api/listings?weight.<criterion>=<0-10>` | The same, with your weights, e.g. `weight.rent_per_m2=5&weight.category:gym=0` |
| `POST /api/listings` | Add a listing: `address`, `warm_rent_eur`, `size_m2`, `rooms`, and optionally `source_url`. Answers `202 Accepted` with the listing, still `pending` |
| `GET /api/listings/{id}` | One listing, with the same `weight.` parameters |
| `DELETE /api/listings/{id}` | Delete one of your listings |
| `GET /api/places` | Your personal places, such as work, each with the `criterion` key it scores under |
| `POST /api/places` | Add a place: `name`, `address`, `mode` (`walk` or `bike`). Answers `202 Accepted` |
| `DELETE /api/places/{id}` | Delete one of your places |
| `GET /api/version` | The commit this server runs, which CI checks after a deploy |

Errors are `{"error": "...", "fields": {"rooms": "..."}}`, with `fields` naming each
invalid field. Status codes: 400 for invalid input, 403 for a request from another site,
404 for a listing or place that isn't yours or doesn't exist, 409 for an eleventh place,
413 and 415 for oversized or non-JSON bodies, 429 with `Retry-After` when a limit is
reached.

## Decisions

Each non-obvious choice, with the options considered and why this one won.

**The service's internals**

- [A job queue in Postgres, not Kafka or Redis](#a-job-queue-in-postgres-not-kafka-or-redis)
- [Claiming jobs: SKIP LOCKED, a lease and a fencing token](#claiming-jobs-skip-locked-a-lease-and-a-fencing-token)
- [Polling instead of LISTEN/NOTIFY](#polling-instead-of-listennotify)
- [Migrations: a small runner instead of a library](#migrations-a-small-runner-instead-of-a-library)
- [Integration tests: one container, one database per test](#integration-tests-one-container-one-database-per-test)
- [Probes and metrics on a separate admin port](#probes-and-metrics-on-a-separate-admin-port)
- [Metric labels use route patterns, not raw paths](#metric-labels-use-route-patterns-not-raw-paths)

**Scoring and the page**

- [Scores use fixed thresholds, not a ranking](#scores-use-fixed-thresholds-not-a-ranking)
- [Scores are worked out on every read](#scores-are-worked-out-on-every-read)
- [Personal places score the trip from each listing](#personal-places-score-the-trip-from-each-listing)
- [The page: no build step, no CDN, a strict content policy](#the-page-no-build-step-no-cdn-a-strict-content-policy)
- [Listings come in through a Source](#listings-come-in-through-a-source)

**Visitors and abuse**

- [Visitors are a cookie, not accounts](#visitors-are-a-cookie-not-accounts)
- [Cross-site request forgery](#cross-site-request-forgery)
- [Limits count additions, under one lock](#limits-count-additions-under-one-lock)

**Map data**

- [The region lives in config, not code](#the-region-lives-in-config-not-code)
- [The region is its exact outline, not a rectangle](#the-region-is-its-exact-outline-not-a-rectangle)
- [Every upstream response is cached, misses included](#every-upstream-response-is-cached-misses-included)
- [Geocoding: streets first without a house number, and the match is shown](#geocoding-streets-first-without-a-house-number-and-the-match-is-shown)
- [Amenities come from our own import, not the Overpass API](#amenities-come-from-our-own-import-not-the-overpass-api)
- [Routing: our own OSRM, built from the same extract](#routing-our-own-osrm-built-from-the-same-extract)

**Images**

- [Three images, built for arm64](#three-images-built-for-arm64)
- [Cross-compiling for arm64](#cross-compiling-for-arm64)
- [distroless/static, running as non-root](#distrolessstatic-running-as-non-root)
- [Everything pinned by digest](#everything-pinned-by-digest)
- [Our own PostGIS image](#our-own-postgis-image)

**The server**

- [Oracle Cloud's free tier, and treating it as revocable](#oracle-clouds-free-tier-and-treating-it-as-revocable)
- [Terraform runs from a laptop, with state kept there](#terraform-runs-from-a-laptop-with-state-kept-there)
- [One server running k3s](#one-server-running-k3s)
- [Pinned, and checked before it runs](#pinned-and-checked-before-it-runs)
- [Oracle's Ubuntu firewall breaks Kubernetes, quietly](#oracles-ubuntu-firewall-breaks-kubernetes-quietly)
- [Locked down by default](#locked-down-by-default)
- [Staying clear of Oracle's idle rule](#staying-clear-of-oracles-idle-rule)

**Deploying**

- [The cluster pulls its deploys](#the-cluster-pulls-its-deploys)
- [Releases are chart versions, not commits that bump a tag](#releases-are-chart-versions-not-commits-that-bump-a-tag)
- [CI waits for the site, not the cluster](#ci-waits-for-the-site-not-the-cluster)
- [HTTPS from Let's Encrypt, on an sslip.io name](#https-from-lets-encrypt-on-an-sslipio-name)
- [One secret, made by hand](#one-secret-made-by-hand)
- [Deploys and lost pods don't drop requests](#deploys-and-lost-pods-dont-drop-requests)
- [A broken release keeps the old one serving](#a-broken-release-keeps-the-old-one-serving)
- [The first install builds its own data](#the-first-install-builds-its-own-data)
- [Rendered in CI the way Flux renders it](#rendered-in-ci-the-way-flux-renders-it)
- [Metrics stay inside the cluster](#metrics-stay-inside-the-cluster)

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

### The page: no build step, no CDN, a strict content policy

The page is one HTML file, a stylesheet and a script of plain modern JavaScript, plus a
copy of Leaflet for the map. All are built into the Go binary with `embed`.

- **No framework or build step.** The page has a form, sliders, a table and a map, and the
  server does all the scoring, so the script only fetches JSON and draws it. A framework
  would add a toolchain to install, update and audit, for no gain at this size.
- **Leaflet is vendored, not loaded from a CDN.** Version 1.9.4, the latest stable
  release, was copied in after checking the download against the hash npm publishes.
  Nothing at runtime depends on another site staying up or uncompromised.
- **A strict Content-Security-Policy.** The page may load scripts, styles and data only
  from itself, plus map tiles from openstreetmap.org. Inline scripts and styles are
  blocked. Text visitors type, such as addresses and place names, is only ever inserted
  with `textContent`, never as HTML. If that rule slipped somewhere, the policy would
  still stop injected script from running.
- **Caching by content.** Each file gets an ETag from a hash of its contents. Browsers keep
  their copy and revalidate it with a cheap `304` until a deploy changes the file.
- **Polling while something is pending.** After adding a listing or place, the page asks
  again every 1.5 seconds until nothing is pending, then stops. Server-sent events would
  push results sooner, but scoring takes about a second, and polling needs nothing on the
  server.
- **Weights live in the browser.** Slider positions are saved in `localStorage` and sent
  as `weight.` parameters. Weights for criteria that no longer exist, such as a deleted
  place, are left out, since the API rejects unknown criteria.

Small details matter on a phone:
- The map stays pinned to the top, so moving a slider visibly recolors the markers.
- `scroll-padding-top` makes focused fields and rows land below the map instead of
  behind it.
- Numbers and units are joined with no-break spaces, so "12.34 €/m²" never splits across
  lines.

Markers show the score as a number on its color, so they read without telling red from
green.

### Listings come in through a Source

The spec rules out scraping listing sites such as ImmoScout24 or Kleinanzeigen, which their
terms forbid. Listings are typed in by hand, through a `Source` interface: a source turns
a submission into a checked draft, and each stored listing records which source it came
from. The add-listing form is the only one. A new input that is allowed, say a partner API,
would be a new `Source`, with nothing downstream changing.

The optional link to the original advert is stored and shown, never fetched. Only
`http` and `https` links are accepted, so a link can't run script when clicked.

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
- **Kept for 30 days after the last visit.** Then the worker's hourly clean-up deletes the
  visitor with their listings and places. People may type in their own address, so
  little is kept, and not for long.
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

### The region lives in config, not code

`config/thuringia.json` holds everything specific to a place:
- the covered area, as a boundary file, and where the map opens
- the language addresses are shown in
- the OpenStreetMap extract to route on
- the thresholds and default weights for rent, size and rooms
- the everyday categories (supermarket, transit stop, pharmacy, doctor, park, train
  station, gym), each with its OSM tags, travel mode, thresholds and weight
- the example listings every visitor sees

Covering another city means writing another file. The file is validated strictly at
startup:
- unknown fields are errors, so a typo can't be silently ignored
- each threshold must point the right way, so a config with "best" and "worst" swapped
  fails instead of quietly inverting a score

Search radii aren't configured. They follow from each category's worst time: if a
20-minute walk already scores 0, there's no point looking further than 20 minutes' walk in
a straight line.

### The region is its exact outline, not a rectangle

"Is this address in the region?" decides whether a listing can be scored at all, since
the routing and amenity data only cover the region. The first version checked a
rectangle around Thuringia. Testing broke it: limited to that rectangle, the geocoder
fuzzy-matched "Alexanderplatz 1, Berlin" to "Kleiner Berlin 1" in Halle. Halle is inside
the rectangle but in Saxony-Anhalt, so the listing got a score from data that doesn't
cover it.

Now the region is the polygon Geofabrik publishes with each extract
(`config/thuringia.poly`, © OpenStreetMap contributors, ODbL), the same line the map
data was cut along. A point-in-polygon test decides what's inside, and the rectangle the
geocoder searches is derived from the polygon, so the two can't disagree. Halle and
Leipzig are now outside, and Eisenach, right at the western border, is inside.

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

### Geocoding: streets first without a house number, and the match is shown

Photon is asked only for buildings and streets, never districts or cities. Otherwise
"Erfurt" would quietly place the listing at the city center and score that.

German listings often leave out the house number, and then the person means the street.
Asked for buildings and streets together, Photon can prefer a landmark on the street or
named after it. Testing turned up three: "Magdeburger Allee" matched a tram line at its
far end, "Gothaer Straße" the trade fair grounds, and "Nordhäuser Straße" the university,
1.5 km from where Photon puts the street itself. So an address without a house number is
looked up among streets first. A street only counts if the address names it, since
Photon matches loosely: "Erfurt" on its own finds an "Erfurter Straße" near Weimar. When
no street matches, buildings and landmarks are searched, so "Universität Erfurt" still
works.

Even then a match can be wrong. A bare "Erfurt" ends up at the main station. So each
listing stores and shows the address it was matched to, and whether it matched a house
or only a street. A wrong match is visible to the user instead of silently scored.
Matches outside the region count as not found, because there's no routing data there.

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
- **Same source data.** Routing already needs the region's OpenStreetMap extract. The extract is filtered down to the seven categories and loaded into an
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

### Three images, built for arm64

CI publishes three images to GHCR:
- **`apartment-finder`**: the api and the worker. The worker's Kubernetes Deployment
  overrides the entrypoint, so both always deploy from the same commit; the cost is a
  few MB of unused binary in each container. It's distroless and tagged with the commit.
- **`apartment-finder-importer`**: the amenity import, run weekly and as the worker's
  first step on a new server, and the routers' download of the map extract. It needs
  osmium, which can't live in a distroless image, so it gets its own Debian-based target
  in the same Dockerfile, with osmium pinned to an exact package version. Also tagged
  with the commit.
- **`apartment-finder-postgis`**: the database, tagged with a hash of its Dockerfile
  rather than the commit. An unchanged database image keeps its tag, so an app deploy
  never restarts Postgres; the image is rebuilt only when its Dockerfile changes.

The region config is built into the app and importer images, so the code and the
config it was tested with always ship together.

Go cross-compiles without help, but the importer and PostGIS images run `apt-get`
inside arm64 images, so CI emulates arm64 with QEMU for those steps. Docker's
`setup-qemu-action` and its emulator image are both pinned.

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

### Our own PostGIS image

The official `postgis/postgis` image is published for amd64 only, and the VM is arm64. A
third-party multi-arch build exists, but its Debian variants stopped being updated in
early 2026, so they were missing months of Postgres security releases.
`deploy/postgis/Dockerfile` uses the same recipe as the official image instead: the
official `postgres:18` image, which is multi-arch and promptly patched, plus PostGIS
from the PostgreSQL project's own apt repository. Both are pinned, so builds are
repeatable, and security updates are an explicit version bump. The integration tests
build and run this same image, so tests and production can't drift apart.

### Oracle Cloud's free tier, and treating it as revocable

The whole stack needs about 4 GB of memory: two routers at 0.4 GB each, Postgres, k3s,
the API and the worker. Of the big clouds, only Oracle gives that away with no end date:
its Always Free tier includes 2 Arm cores and 12 GB of memory.
- **AWS** now gives new accounts $100 to $200 of credits for six months, after which the
  account closes unless upgraded; a 4 GB Arm server then costs about $30 a month.
- **Google Cloud** and **Azure** give away 1 GB servers, too small for this stack.

Oracle can change its free tier, and did in 2026, halving the free Arm allowance to what
this project uses. So the deployment is built to be lost:
- **Everything is code.** `terraform destroy` followed by `terraform apply` rebuilds the
  server in under a minute, and that cycle has been run for real.
- **The data comes back.** Amenities, routing data and examples are rebuilt from
  OpenStreetMap; visitors' data lasts 30 days anyway.
- **Only one layer is Oracle's.** Kubernetes, the images and CI run on any Linux server.
  Each Terraform resource has a direct AWS equivalent: virtual cloud network for VPC,
  security list for security group, internet gateway for internet gateway, instance for
  EC2.

The home region is Frankfurt, which can never be changed: it's closest to Thuringia,
keeps visitors' data in the EU, and has three availability domains to try when one runs
out of free Arm capacity. Every other EU region has one.

### Terraform runs from a laptop, with state kept there

Terraform needs Oracle API credentials. Running it only from a laptop keeps them out of
GitHub entirely: CI checks the Terraform (`fmt` and `validate`, which need no account)
but never applies it.

The state, Terraform's record of what it created, stays on the laptop too, gitignored.
With one person running Terraform, a shared state backend adds a bucket to bootstrap for
little gain. If the state were lost, the server would keep running and could be imported
or rebuilt.

### One server running k3s

Kubernetes on one server is k3s: a single binary with the pieces a small cluster needs
built in, including the Traefik ingress, a load balancer that uses the server's own ports,
and local disk volumes. It keeps cluster state in SQLite instead of running etcd. The
whole cluster, with Traefik and DNS, uses about 1.2 GB of the server's 12.

### Pinned, and checked before it runs

- **The OS image** is chosen by its exact name, so rebuilding gives the same server; it
  keeps itself patched with Ubuntu's automatic security updates in between.
- **k3s** is a pinned release, and both its binary and its install script are checked
  against SHA-256 hashes in the Terraform before anything runs. A tampered download
  stops the setup.
- **Terraform in CI** is the same version as locally, checked against HashiCorp's
  published hash.

### Oracle's Ubuntu firewall breaks Kubernetes, quietly

Oracle's Ubuntu images come with their own iptables rules that reject everything except
SSH, on top of the cloud firewall. They also reject all forwarded traffic, which is how
Kubernetes moves packets to pods. So a fresh cluster looks healthy, but web traffic never
reaches it, and pods can't talk to each other.

The first-boot script fixes this before k3s starts:
- it stops rejecting forwarded traffic
- it lets pods and services (`10.42.0.0/16`, `10.43.0.0/16`) reach the server
- it opens ports 80 and 443

The cloud firewall still decides what gets in from outside. This was checked end to end:
a test page behind Traefik answered over the public IP, a pod resolved names through the
cluster's DNS, and everything still worked after a reboot.

### Locked down by default

- **Only SSH, HTTP and HTTPS** get through the cloud firewall. The Kubernetes API (6443)
  and the kubelet (10250) were checked and are unreachable from outside; `kubectl` goes
  through an SSH tunnel.
- **SSH is keys only.** Port 22 is open to anyone so it works from anywhere, but password
  and root logins are off, so there is nothing to guess.
- **The cloud metadata service** answers only its token-based version, which makes it
  harder for a request forged through an app on the server to read the server's
  credentials.
- **Kubernetes Secrets are encrypted** on disk.

The server's public IP is ephemeral: free with the server, but replaced if the server is
replaced. Oracle's docs don't list a reserved IP as Always Free, so the deployment's DNS
follows the IP instead.

One more free-tier rule: Oracle may reclaim a server whose CPU, network and memory all
stay under 20% for a week. See "Staying clear of Oracle's idle rule" below.

### Staying clear of Oracle's idle rule

Oracle may reclaim an Arm server whose CPU, network and memory all stay under 20% for a
week. CPU and network will, for a site this quiet, so memory decides.

The plan was a 1 GB Postgres cache, but Linux only counts shared memory once it's used,
and this little data never fills it. With the app deployed, the server measured 22%,
too close to the line. Prometheus and Grafana from synthetic-monitor, running on the
same server, should add enough; the server will be measured again once they're running.
The other options considered:
- **A Pay As You Go account**, which Always Free resources stay free on. A 2023 notice
  from Oracle said this prevents reclaiming, but its current docs don't say so.
- **A pod that just holds memory.** Predictable, but it exists only to get past the
  check.

### The cluster pulls its deploys

CI never touches the cluster. Flux, running inside it, pulls what git and the registry
say should run:
- **CI holds no cluster credentials**, and the Kubernetes API stays closed to the
  internet. What CI can do is publish images and charts, with a token that expires when
  its job ends.
- **Git is the record.** The cluster's setup (cert-manager, the certificate issuers,
  Traefik's settings, the release) lives in `deploy/flux`, and Flux puts back anything
  there changed by hand. A hand change to the app's own objects lasts until the next
  release.
- **The catch:** CI can't watch the rollout. It watches the site's version instead (see
  "CI waits for the site, not the cluster" below).

Flux, not Argo CD: both do this, but Argo CD is built around a web UI and runs more
components. Flux is four controllers configured in YAML, using about 110 MB here, which
suits one small server with nobody watching a dashboard.

### Releases are chart versions, not commits that bump a tag

CI publishes the Helm chart to GHCR as an OCI artifact, versioned `0.1.<run number>`,
with the commit as its app version, which picks the image tag. Flux deploys the highest
version.

The usual alternative has CI commit each new image tag back to the repo for Flux to pick
up. That needs a bot with write access to `main` and doubles its commits. Here the chart
and images are built and released together, and git holds only what doesn't change per
release: which chart to follow, and the server's settings.

### CI waits for the site, not the cluster

Since CI can't see inside the cluster, its last job polls `/api/version` until the site
serves the commit. What that does and doesn't prove:
- A release that fails keeps the old version serving, so CI fails with a message saying
  where to look.
- A newer commit that's live counts too, since it includes this one. When commits land
  close together, the older run's check shouldn't fail just because it was overtaken.
  A revert counts as newer as well, so a broken commit's check fails only if nothing
  replaces it within the 15 minutes.
- It proves the API is serving the commit, not that the whole release succeeded. If the
  API comes up but the worker doesn't, Helm rolls everything back after CI has passed.
  Flux can report each release's outcome to GitHub as a commit status, which would close
  that gap, at the cost of a GitHub token kept in the cluster.

### HTTPS from Let's Encrypt, on an sslip.io name

- **cert-manager** gets and renews the certificate, proving control of the name over
  plain HTTP through Traefik. Traefik redirects all other HTTP to HTTPS.
- **sslip.io** is a public DNS service that answers `92-5-186-10.sslip.io` with
  `92.5.186.10`. There is no domain to buy and no DNS to update when the IP changes, only
  the name in the config.
- **The rate limit:** Let's Encrypt caps certificates per registered domain, and
  sslip.io isn't on the Public Suffix List, so every sslip.io user shares one limit.
  sslip.io's documentation says Let's Encrypt raised it for them. The issuer was tested
  against Let's Encrypt's staging service first. If the limit ever bites, DuckDNS, which
  is on the list, is the fallback.
- **No email on the Let's Encrypt account.** Let's Encrypt stopped sending expiry
  warnings in 2025, and renewal is automatic.

### One secret, made by hand

The only secret is the database password. It's generated inside the cluster at setup
and exists only as a Kubernetes Secret, which k3s encrypts on disk. Sealed Secrets or
SOPS would keep an encrypted copy in git, so a rebuilt cluster gets it back. With one
password, and a database that is rebuilt from scratch with the server anyway, that adds
a key to manage and protects nothing.

### Deploys and lost pods don't drop requests

- **Two API pods.** The rolling update starts a new one and waits until it's ready
  before stopping an old one.
- **A stopping pod waits 5 seconds** before shutting down, so Traefik has stopped
  sending it requests by then.
- **A disruption budget** keeps at least one API pod through planned disruptions, such
  as draining the server.

Checked: deleting an API pod while sending requests through the public URL, 307 requests
during the deletion and replacement were all answered.

The worker is the exception: exactly one, stopped before its replacement starts. The
geocoder's rate limit lives in its memory, so two at once would double the request rate.
Jobs wait in Postgres for the few seconds it's away.

### A broken release keeps the old one serving

Tested for real by merging an image that can't start, with an entrypoint naming a missing
binary, and reverting it afterwards (pull requests #23 to #25):
- **The old version kept serving.** The new API pod never got ready, so the rolling
  update never stopped an old one. A request every 2 seconds, for the 38 minutes of two
  failed releases, their rollbacks and the revert's deploy, got an answer every time:
  1,028 of 1,028.
- **The deploy failed visibly.** Helm's upgrade timed out after 10 minutes, and CI's
  deploy check failed, saying the site still served the previous commit.
- **Flux rolled the release back** to the last good one, removing the broken pod.

The test caught a bug on the way. Flux was set to roll back without retrying, and with
no retries it rolls back only if also told to remediate the last failure. It had left
the failed release in place, with the broken pod crash-looping beside the old ones,
until `remediateLastFailure` was set.

### The first install builds its own data

- **Each router** has a setup step that downloads the extract and builds its routing
  data onto the router's own volume, then leaves a marker. Later restarts find the
  marker and start in seconds.
- **The worker** has a setup step that imports the everyday places if the table is
  empty, so it never scores listings against nothing. A weekly job refreshes them.

Building the routing data in CI and shipping it in an image was the alternative. It
would make each deploy carry about 1.3 GB of data that changes weekly at most, and tie
refreshing the map to releasing code.

The import's memory limit was set by measuring on the server: osmium's filter peaks at
2 GiB there, because keeping the nodes that matching ways are made of takes a set sized
to the whole range of OpenStreetMap node IDs. On a laptop, the same command reported
42 MB. The first install hit the old 1 GiB limit, which is how that came to light.

### Rendered in CI the way Flux renders it

The first install failed on the cluster although CI had passed: the pod templates listed
two labels twice. Helm quietly keeps the last copy; Flux runs every release through
kustomize, which rejects it. CI now renders the chart through kustomize too, so that
mistake fails a pull request instead.

### Metrics stay inside the cluster

`/metrics` is served on the admin port, through its own Service, which no ingress rule
points at. Checked from outside: `/metrics` on the site answers 404 and port 9090 doesn't
answer at all. From a pod inside the cluster, both the API and the worker answer.
Prometheus and Grafana run on the same server but live in a separate repo,
[synthetic-monitor](https://github.com/MSJawad/synthetic-monitor).
