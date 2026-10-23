# apartment-finder

Scores apartment listings by how close they are to the places you'd actually go:
supermarkets, bus and tram stops, pharmacies, doctors, parks, the train station and the
gym. You add a listing, and a few seconds later it's on the map with a score out of 100
and a breakdown of where that score came from. Sliders let you change what matters to
you.

When I moved to Erfurt, I did all of this in a Google Sheet with some Apps Script. This
is that sheet rebuilt as a proper service: Go, Postgres with PostGIS, a self-hosted
router, and Kubernetes on Oracle Cloud.

It's live at **https://92-5-186-10.sslip.io**.

## How it works

Everything runs on one Arm server:

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

When you add a listing, the API saves it along with a job to process it, and returns
right away. The worker picks up the job, geocodes the address with Photon, finds the
nearest of each kind of place in PostGIS, and asks OSRM how long the walk or bike ride to
each one takes. The page keeps checking until the listing is done, which takes about a
second.

Each criterion is scored between two fixed thresholds. A supermarket 5 minutes' walk away
gets full marks, one 20 minutes away gets nothing, and anything in between is scaled.
Rent per m², size and rooms work the same way, and the total is a weighted average. You
can also add up to 10 places of your own, like work or uni, and the trip to each one
becomes another criterion just for you.

There are no accounts. You get a cookie with a random id, and your listings are deleted
30 days after your last visit.

Merging to `main` is the whole deploy. GitHub Actions runs the tests, builds arm64 images
and publishes them to GHCR along with the Helm chart. Flux, running inside the cluster,
picks up the new chart version and upgrades the release. If the new version doesn't come
up, Flux rolls it back and the old one keeps serving.

Logs are JSON, and Prometheus metrics are served on a separate port that isn't reachable
from the internet. Prometheus and Grafana themselves live in my
[synthetic-monitor](https://github.com/MSJawad/synthetic-monitor) repo, on the same
server.

## Running it locally

You need Go 1.27, Docker and [osmium](https://osmcode.org/osmium-tool/).

```sh
go test -race ./...            # the integration tests start their own PostGIS container

scripts/prepare-routing.sh     # downloads the map and builds the routing data, ~4 min
docker compose up -d --wait    # Postgres and the two routers
export DATABASE_URL='postgres://postgres:dev@localhost:5432/postgres?sslmode=disable'
export REGION_CONFIG=config/thuringia.json
go run ./cmd/import-amenities data/osm/region.osm.pbf

go run ./cmd/api               # http://localhost:8080

# in another terminal, with the same two exports
OSRM_WALK_URL=http://localhost:5001 OSRM_BIKE_URL=http://localhost:5002 ADMIN_ADDR=:9091 \
  go run ./cmd/worker
```

## Deploying

You need an Oracle Cloud account with Frankfurt as its home region, an API key in
`~/.oci/config`, Terraform and the Flux CLI.

```sh
cd deploy/terraform
cp terraform.tfvars.example terraform.tfvars   # add your tenancy_ocid
terraform apply                                # creates the server, prints its IP
ssh ubuntu@<ip> cloud-init status --wait       # waits for k3s to install

# The Kubernetes API isn't open to the internet, so kubectl goes through SSH
ssh ubuntu@<ip> sudo cat /etc/rancher/k3s/k3s.yaml \
  | sed 's#127.0.0.1:6443#127.0.0.1:16443#' > ~/.kube/apartment-finder.yaml
ssh -fN -L 16443:127.0.0.1:6443 ubuntu@<ip>
export KUBECONFIG=~/.kube/apartment-finder.yaml

# The database password only ever exists inside the cluster
kubectl create namespace apartment-finder
kubectl -n apartment-finder create secret generic apartment-finder-db \
  --from-literal=password="$(openssl rand -hex 24)"

GITHUB_TOKEN=$(gh auth token) flux bootstrap github --owner=MSJawad \
  --repository=apartment-finder --branch=main --path=deploy/flux/clusters/production \
  --personal --read-write-key=false
```

A new server gets a new IP, so put it (with dashes) in `host` in
`deploy/flux/apps/apartment-finder.yaml` and in the repo's `SITE_URL` variable. The first
install takes about 8 minutes, since the routers build their data on the server.

If Terraform says "Out of host capacity", Oracle has run out of Arm servers in that
availability domain. Set `availability_domain = 2` or `3` in `terraform.tfvars` and try
again.

## Decisions

### Why the job queue is just a Postgres table

Jobs are rows in a table, and workers claim them with `FOR UPDATE SKIP LOCKED` so two
workers never grab the same one. A claim is a lease with a timeout rather than an open
transaction, so if a worker dies mid-job, the job just gets picked up again.

Kafka or Redis would be one more stateful thing to run and look after on a 12 GB server,
for maybe a few hundred jobs a day. The main reason though is that a listing and its job
are inserted in the same transaction, so one can't exist without the other. With a
separate broker you'd need an outbox table to get that guarantee, which is basically a
Postgres queue anyway. If this ever needed thousands of jobs a second, I'd look at a
broker then.

### Why not the Overpass API

The original plan was to look up nearby places with Overpass. When I tried it, the public
server was at capacity, and its own error page says automated use should run its own
instance. The OpenStreetMap wiki also asks regular automated users to stay around 100
queries a day.

So instead, the seven categories are filtered out of the same Geofabrik extract the
routers use and loaded into PostGIS, and a weekly job refreshes them. Finding the nearest
supermarket is then one indexed query. The data can be up to a week old, which is fine
for supermarkets.

### Why I run my own router

The public OSRM servers allow about one request a second and ask for no heavy use, so a
worker running all day shouldn't depend on them. Instead there are two OSRM routers, one
for walking and one for cycling, built from the Thuringia extract. Each uses about 0.4 GB
of memory, and on the trips I checked they came within 20% of openstreetmap.org's
directions.

### Going easy on Photon

Photon's public server is shared, and its usage policy asks you to be fair with it. Every
response is cached in Postgres, including the ones that found nothing, so a typo isn't
looked up again every time a job retries. Requests are rate limited, and the whole site
accepts at most 300 new addresses a day, so that's the most Photon will ever get from it.

### Scores are worked out on every request

The worker only stores travel times, never scores. Every page load scores the listings
from those times and your weights, so moving a slider is just another request and nothing
gets measured again. The thresholds are fixed instead of relative to the other listings,
so adding a listing never changes anyone else's score.

### One server that can be rebuilt from scratch

Everything fits on one Arm server with 2 cores and 12 GB of memory. The server is all
Terraform, and the map data rebuilds itself from OpenStreetMap, so a new one only needs
the setup above. I've destroyed and recreated it to make sure that actually works.

Everything has to run on arm64. Most images already do, but the official PostGIS image is
amd64 only, so `deploy/postgis` builds one from the official Postgres image.

### The cluster pulls its own deploys

CI never has credentials for the cluster or for Oracle, and the Kubernetes API isn't
reachable from the internet. Flux runs inside the cluster and pulls new releases from GHCR
instead. I picked Flux over Argo CD because it's lighter and doesn't need a web UI.

The downside is that CI can't watch the rollout, so its last step polls the site's
`/api/version` until it shows the new commit. I tested a broken release on purpose: the
old version answered every request the whole time, CI went red, and Flux rolled it back.

### HTTPS without a domain

The site's name comes from sslip.io, which resolves `92-5-186-10.sslip.io` to
`92.5.186.10`, so there's no DNS to manage. cert-manager gets a Let's Encrypt certificate
for that name and renews it automatically.

## What's missing

- No backups. Visitors' listings are deleted after 30 days anyway, and everything else
  rebuilds from OpenStreetMap, but losing the server would still lose whatever people
  have added.
- It's one server, so if it goes down, so does the site.
- The hostname is the server's IP, so it changes if the server is ever replaced.
- CI checks that the API is serving the new commit, not that every part of the release
  worked.
