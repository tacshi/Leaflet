#!/usr/bin/env bash
# Builds a debug Leaflet that runs beside a release copy: it keeps MyGo's
# development features (such as the web inspector), and is named "Leaflet
# Debug" with the identifier app.capyvibe.leaflet.debug, so it has a data
# folder, Keychain entries and single-instance lock of its own. Its pages
# and settings start empty; LEAFLET_DATA_DIR still points it elsewhere. It
# never updates itself, so a release never replaces it.
#
#   scripts/build-debug.sh          # macOS, for this Mac's architecture
#   scripts/build-debug.sh --open   # build, then launch it
#   scripts/build-debug.sh --win    # Windows x64, with its installer
#   scripts/build-debug.sh --win --arch arm64
#
# macOS builds go to build/mac-debug/Leaflet Debug.app, signed ad hoc.
# Windows builds go to build/win-debug/windows-<arch>: Leaflet Debug.exe
# with its files, and "Leaflet Debug Setup <version> <arch>.exe" when NSIS
# is installed (brew install makensis). Neither is signed for release.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/release.sh"
cd "$ROOT_DIR"

TARGET=mac
ARCH=""
OPEN=false
while (($#)); do
	case "$1" in
	--mac) TARGET=mac ;;
	--win) TARGET=win ;;
	--arch)
		[[ $# -ge 2 ]] || die "--arch needs arm64 or x64"
		ARCH="$2"
		shift
		;;
	--open) OPEN=true ;;
	-h | --help)
		sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*) die "unknown option: $1" ;;
	esac
	shift
done

command -v go >/dev/null 2>&1 || die "needs go"
command -v python3 >/dev/null 2>&1 || die "needs python3"
if [[ -z "$ARCH" ]]; then
	if [[ "$TARGET" == mac ]]; then
		case "$(uname -m)" in
		arm64) ARCH=arm64 ;;
		x86_64) ARCH=x64 ;;
		*) die "unsupported architecture: $(uname -m)" ;;
		esac
	else
		ARCH=x64
	fi
fi
case "$ARCH" in
arm64) GOARCH=arm64 ;;
x64 | amd64) GOARCH=amd64 ;;
*) die "unknown --arch $ARCH: arm64 or x64" ;;
esac

NAME="Leaflet Debug"
# MyGo names the data folder after the app and keeps secrets under its
# identifier, both from mygo.json, which this build alone sees changed.
# Without updates, the app has no updater.
patch_config "name=$NAME" "identifier=$(config_value identifier).debug" -updates

if [[ "$TARGET" == mac ]]; then
	[[ "$(uname -s)" == "Darwin" ]] || die "macOS apps are built on macOS; --win builds for Windows"
	OUT="build/mac-debug"
	BUILT="$OUT/darwin-$GOARCH/$NAME.app"
	APP="$OUT/$NAME.app"
	info "building $NAME for darwin/$GOARCH"
	go tool mygo build -debug -skip-dmg -sign - -platform "darwin/$GOARCH" -o "$OUT"
	[[ -d "$BUILT" ]] || die "expected $BUILT after the build"
	rm -rf "$APP"
	mv "$BUILT" "$APP"
	# MyGo builds into a folder per platform, empty once the app moves out.
	rmdir "$(dirname "$BUILT")" 2>/dev/null || true
	info "built $ROOT_DIR/$APP"
	if $OPEN; then open "$APP"; fi
else
	$OPEN && die "--open launches macOS builds only"
	command -v makensis >/dev/null 2>&1 || warn "NSIS is not installed, so there will be no installer (brew install makensis)"
	utf8_locale
	OUT="build/win-debug"
	info "building $NAME for windows/$GOARCH"
	go tool mygo build -debug -platform "windows/$GOARCH" -o "$OUT"
	[[ -f "$OUT/windows-$GOARCH/$NAME.exe" ]] || die "expected $OUT/windows-$GOARCH/$NAME.exe after the build"
	info "built $ROOT_DIR/$OUT/windows-$GOARCH"
fi
