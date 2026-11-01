#!/usr/bin/env bash
# Builds leaflet-speech, the helper the Mac app transcribes recordings with
# on the Mac itself (macos/speech), into resources/darwin-<arch>, which
# mygo build and mygo dev ship in that architecture's app, in
# Contents/Resources. The build scripts run it; run it before mygo dev to
# record with On device there.
#
#   scripts/build-speech.sh            # this Mac's architecture
#   scripts/build-speech.sh arm64 x64  # each of those
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib/release.sh"

[[ "$(uname -s)" == "Darwin" ]] || die "leaflet-speech is built on macOS"
command -v xcrun >/dev/null 2>&1 || die "needs Xcode's command line tools"

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
	# It runs on the app's oldest macOS, and says there that it can't
	# transcribe: SpeechTranscriber is macOS 26's.
	xcrun swiftc -O -swift-version 6 -parse-as-library -target "$target-apple-macos12" \
		"$ROOT_DIR/macos/speech/main.swift" -o "$out"
done
