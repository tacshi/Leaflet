#!/bin/bash
# Builds the Go core into ios/build/LeafletCore.xcframework with gomobile,
# for the app and its tests to link.
#
#   ios/scripts/build-core.sh              # devices and the simulator
#   ios/scripts/build-core.sh simulator    # the simulator only, as CI does

set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
out="$root/ios/build"
bin="$out/bin"

case "${1:-all}" in
	all) targets=ios,iossimulator ;;
	simulator) targets=iossimulator ;;
	*) echo "usage: $0 [simulator]" >&2; exit 2 ;;
esac

mkdir -p "$bin"
cd "$root"

# gomobile runs gobind from PATH, so both are built from the versions go.mod
# pins, rather than whatever is installed.
go build -o "$bin/gomobile" golang.org/x/mobile/cmd/gomobile
go build -o "$bin/gobind" golang.org/x/mobile/cmd/gobind

# Bound into a folder of its own first, and put in place only once the
# bind succeeds: a core that doesn't build leaves the last one, for the
# app and its tests to go on building against.
tmp=$(mktemp -d "$out/core.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
PATH="$bin:$PATH" CGO_ENABLED=1 "$bin/gomobile" bind \
	-target "$targets" \
	-iosversion 26.0 \
	-o "$tmp/LeafletCore.xcframework" \
	./mobile
rm -rf "$out/LeafletCore.xcframework"
mv "$tmp/LeafletCore.xcframework" "$out/LeafletCore.xcframework"
