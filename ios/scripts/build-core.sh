#!/bin/bash

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

go build -o "$bin/gomobile" golang.org/x/mobile/cmd/gomobile
go build -o "$bin/gobind" golang.org/x/mobile/cmd/gobind

tmp=$(mktemp -d "$out/core.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
PATH="$bin:$PATH" CGO_ENABLED=1 "$bin/gomobile" bind \
	-target "$targets" \
	-iosversion 26.0 \
	-o "$tmp/LeafletCore.xcframework" \
	./mobile
rm -rf "$out/LeafletCore.xcframework"
mv "$tmp/LeafletCore.xcframework" "$out/LeafletCore.xcframework"
