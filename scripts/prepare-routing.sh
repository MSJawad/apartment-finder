#!/bin/sh
# Downloads the region's OpenStreetMap extract and builds the walking and
# cycling routing data that the osrm-walk and osrm-bike services in
# docker-compose.yml serve.
#
# Usage: scripts/prepare-routing.sh [region config]
#
# The extract is kept in data/osm and reused while its checksum holds; delete
# data/osm to fetch a newer one.
set -eu
cd "$(dirname "$0")/.."

config=${1:-config/thuringia.json}
url=$(sed -n 's/^ *"osm_extract": *"\([^"]*\)".*/\1/p' "$config")
if [ -z "$url" ]; then
	echo "no osm_extract in $config" >&2
	exit 1
fi

pbf=data/osm/region.osm.pbf

md5_of() {
	if command -v md5sum >/dev/null; then md5sum "$1"; else md5 -r "$1"; fi | cut -d' ' -f1
}

verified() {
	[ -f "$pbf" ] && [ -f "$pbf.md5" ] && [ "$(md5_of "$pbf")" = "$(cut -d' ' -f1 "$pbf.md5")" ]
}

if ! verified; then
	mkdir -p data/osm
	# Geofabrik's "latest" URL redirects to a dated file, and its checksum can
	# change between two requests. Resolving the redirect first makes the
	# extract and the checksum come from the same day.
	dated=$(curl -fsSIL -o /dev/null -w '%{url_effective}' "$url")
	echo "Downloading $dated"
	curl -fL -o "$pbf.md5" "$dated.md5"
	curl -fL -o "$pbf" "$dated"
	if ! verified; then
		echo "checksum mismatch for $dated" >&2
		exit 1
	fi
fi

# Runs a command in the OSRM image, as the current user so the files it
# writes aren't owned by root.
osrm() {
	docker compose run --rm --user "$(id -u):$(id -g)" osrm-prepare "$@"
}

for mode in walk bike; do
	case $mode in
	walk) profile=foot ;;
	bike) profile=bicycle ;;
	esac
	echo "Preparing $mode routing with the $profile profile"
	rm -rf "data/routing/$mode"
	mkdir -p "data/routing/$mode"
	# osrm-extract writes its output next to its input.
	cp "$pbf" "data/routing/$mode/region.osm.pbf"
	osrm osrm-extract -p "/opt/$profile.lua" "/data/routing/$mode/region.osm.pbf"
	osrm osrm-partition "/data/routing/$mode/region.osrm"
	osrm osrm-customize "/data/routing/$mode/region.osrm"
	rm "data/routing/$mode/region.osm.pbf"
done

echo "Done. Start or restart the routers with: docker compose up -d osrm-walk osrm-bike"
