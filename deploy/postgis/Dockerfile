# PostgreSQL 18 with PostGIS, for arm64 and amd64.
#
# The official postgis/postgis image is published for amd64 only. This uses
# the same recipe: the official postgres image plus PostGIS from the
# PostgreSQL project's own apt repository, which that image already trusts and
# which builds for both architectures. Both are pinned, so builds are
# repeatable; security updates mean bumping the digest and version here.
FROM postgres:18-trixie@sha256:1267c7e692de235fda2a50dbcdbcbca4672ea6a0298576a6bd9d07cafc319b32

ARG POSTGIS_VERSION=3.6.4+dfsg-2.pgdg13+1
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        postgresql-18-postgis-3="$POSTGIS_VERSION" \
        postgresql-18-postgis-3-scripts="$POSTGIS_VERSION" \
    && rm -rf /var/lib/apt/lists/*
