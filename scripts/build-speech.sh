#!/usr/bin/env bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib/release.sh"

[[ "$(uname -s)" == "Darwin" ]] || die "leaflet-speech is built on macOS"
why="$(speech_toolchain)" || die "leaflet-speech can't be built here: $why"

ARCHES=("$@")
if ((${#ARCHES[@]} == 0)); then
	case "$(uname -m)" in
	arm64) ARCHES=(arm64) ;;
	*) ARCHES=(x64) ;;
	esac
fi
for arch in "${ARCHES[@]}"; do
	case "$arch" in
	arm64) target=arm64 goarch=arm64 ;;
	x64 | amd64) target=x86_64 goarch=amd64 ;;
	*) die "unknown architecture $arch: arm64 or x64" ;;
	esac
	out="$ROOT_DIR/resources/darwin-$goarch/leaflet-speech"
	mkdir -p "$(dirname "$out")"
	info "building leaflet-speech for darwin/$goarch"
	lib="$ROOT_DIR/build/speech/$goarch"
	mkdir -p "$lib"
	xcrun swiftc -O -swift-version 6 -parse-as-library -target "$target-apple-macos13" \
		-module-name LeafletSpeech -emit-library -static -emit-module \
		-emit-module-path "$lib/LeafletSpeech.swiftmodule" -o "$lib/libLeafletSpeech.a" \
		"$ROOT_DIR"/speech/Sources/LeafletSpeech/*.swift
	xcrun swiftc -O -swift-version 6 -parse-as-library -target "$target-apple-macos13" \
		-I "$lib" -L "$lib" -lLeafletSpeech "$ROOT_DIR"/speech/Sources/leaflet-speech/*.swift -o "$out"
done
