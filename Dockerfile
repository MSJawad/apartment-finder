# Two images from one build:
#   - app (the default): api and worker, on distroless
#   - importer (--target importer): the weekly amenity import, which needs
#     osmium and so a Debian base
# Both carry the region config under /etc/apartment-finder.

# The build stage runs on the build machine's own CPU and cross-compiles for
# the target, so an arm64 image builds on an amd64 CI runner without emulation.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:f24164268fc1e741bbdc0066dbadcf1c8426c5a8489f0729c02c3905a53032fd AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/ ./cmd/...

FROM debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad AS importer
ARG OSMIUM_VERSION=1.18.0-1
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates osmium-tool="$OSMIUM_VERSION" \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/import-amenities /out/download-extract /usr/local/bin/
COPY config /etc/apartment-finder
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/import-amenities"]

# distroless/static has no shell or package manager, only CA certificates,
# timezone data and a non-root user: all a static Go binary needs.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087 AS app
COPY --from=build /out/ /usr/local/bin/
COPY config /etc/apartment-finder
USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/api"]
