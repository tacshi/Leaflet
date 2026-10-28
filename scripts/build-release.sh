#!/usr/bin/env bash
# Builds Leaflet's release installers, with the signed update archives,
# deltas and manifests that installed copies update from.
#
#   scripts/build-release.sh                  # macOS and Windows, the version in mygo.json
#   scripts/build-release.sh 0.2.0            # another version
#   scripts/build-release.sh --mac            # macOS only
#   scripts/build-release.sh --win            # Windows only
#   scripts/build-release.sh --arch arm64     # only some: arm64, x64, universal
#   scripts/build-release.sh --skip-notarize  # macOS signed, not notarized (faster)
#   scripts/build-release.sh -o out           # another output directory
#   scripts/build-release.sh --upload         # and upload to a draft GitHub release
#   scripts/build-release.sh --changelog f.md # notes from f.md, not CHANGELOG.md
#
# The installers go to build/release: Leaflet-<version>-{arm64,x64,universal}.dmg
# for macOS (Apple silicon, Intel, both) and Leaflet-<version>-{x64,arm64}-setup.exe
# for Windows, beside a folder per platform with the app and its updates.
# --arch picks among each platform's: universal is macOS only, so
# --arch universal alone builds macOS only. --upload puts
# them all in the draft release v<version> of tacshi/Leaflet, with gh;
# installed copies see the update once the draft is published.
#
# Signs updates with MYGO_UPDATER_PRIVATE_KEY, else the key in the file
# LEAFLET_UPDATE_KEY, by default
#   ~/Library/Application Support/mygo/update-keys/Leaflet/mygo-update.key
# made with
#   go tool mygo keygen -o ~/Library/Application\ Support/mygo/update-keys/Leaflet
# Its public half is updates.publicKey in mygo.json.
#
# macOS: signs with DEVELOPER_ID_APPLICATION, or the keychain's one Developer
# ID Application identity when it is not set, and notarizes with a notarytool
# keychain profile: LEAFLET_NOTARY_PROFILE, "Leaflet" by default, made with
#   xcrun notarytool store-credentials Leaflet --apple-id <id> --team-id <team>
#
# Windows: needs NSIS (brew install makensis) for the installers. Signs with
# the .pfx LEAFLET_WINDOWS_CERTIFICATE names, whose password is
# MYGO_WINDOWS_CERTIFICATE_PASSWORD, using osslsigncode; without it the
# builds are unsigned and SmartScreen warns about them.
#
# To build a Leaflet server in, set MYGO_LDFLAGS as the README says.
# scripts/publish.sh runs this with --changelog, -o and --upload.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/release.sh"
cd "$ROOT_DIR"

VERSION=""
MAC=false
WIN=false
ARCHES=()
NOTARIZE=true
UPLOAD=false
CHANGELOG=""
OUT_DIR="$ROOT_DIR/build/release"
while (($#)); do
	case "$1" in
	--mac) MAC=true ;;
	--win) WIN=true ;;
	--arch)
		[[ $# -ge 2 ]] || die "--arch needs arm64, x64 or universal"
		ARCHES+=("$2")
		shift
		;;
	--skip-notarize) NOTARIZE=false ;;
	--upload) UPLOAD=true ;;
	--changelog)
		[[ $# -ge 2 ]] || die "--changelog needs a file"
		CHANGELOG="$2"
		shift
		;;
	-o)
		[[ $# -ge 2 ]] || die "-o needs a directory"
		OUT_DIR="$2"
		shift
		;;
	-h | --help)
		sed -n '2,41p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	-*) die "unknown option: $1" ;;
	*)
		[[ -z "$VERSION" ]] || die "one version, please: $VERSION or $1"
		VERSION="${1#v}"
		;;
	esac
	shift
done
# Both, unless one is asked for.
PICKED=true
if ! $MAC && ! $WIN; then
	MAC=true
	WIN=true
	PICKED=false
fi

[[ "$(uname -s)" == "Darwin" ]] || die "releases are built on macOS"
for tool in go python3; do
	command -v "$tool" >/dev/null 2>&1 || die "needs $tool"
done
[[ -n "$VERSION" ]] || VERSION="$(config_value version)"
check_version "$VERSION"
case "$OUT_DIR" in /*) ;; *) OUT_DIR="$ROOT_DIR/$OUT_DIR" ;; esac
if [[ -n "$CHANGELOG" ]]; then
	[[ -f "$CHANGELOG" ]] || die "no changelog $CHANGELOG"
	CHANGELOG="$(cd "$(dirname "$CHANGELOG")" && pwd)/$(basename "$CHANGELOG")"
fi

# Each platform's architectures: those --arch names that it has, or all.
# Arrays that may be empty expand as ${a[@]+"${a[@]}"}: macOS's bash 3.2
# takes an empty "${a[@]}" for an unbound variable under set -u.
MAC_ARCHES=()
WIN_ARCHES=()
for arch in ${ARCHES[@]+"${ARCHES[@]}"}; do
	case "$arch" in
	arm64 | x64 | amd64 | universal) ;;
	*) die "unknown --arch $arch: arm64, x64 or universal" ;;
	esac
	if [[ "$arch" == amd64 ]]; then arch=x64; fi
	if $MAC; then MAC_ARCHES+=("$arch"); fi
	if $WIN && [[ "$arch" != universal ]]; then WIN_ARCHES+=("$arch"); fi
done
if $MAC && ((${#MAC_ARCHES[@]} == 0)); then MAC_ARCHES=(arm64 x64 universal); fi
if $WIN && ((${#ARCHES[@]} == 0)); then WIN_ARCHES=(x64 arm64); fi
if $WIN && ((${#WIN_ARCHES[@]} == 0)); then
	$PICKED && die "Windows has no universal build; --arch arm64 or x64"
	WIN=false
fi

PLATFORMS=()
goarch() { if [[ "$1" == x64 ]]; then echo amd64; else echo "$1"; fi; }
if $MAC; then for arch in "${MAC_ARCHES[@]}"; do PLATFORMS+=("darwin/$(goarch "$arch")"); done; fi
if $WIN; then for arch in "${WIN_ARCHES[@]}"; do PLATFORMS+=("windows/$(goarch "$arch")"); done; fi

CONFIG=("version=$VERSION")
[[ -z "$CHANGELOG" ]] || CONFIG+=("updates.changelog=$CHANGELOG")
BUILD_ARGS=()
if $MAC; then
	for tool in xcrun security; do
		command -v "$tool" >/dev/null 2>&1 || die "needs $tool"
	done
	DEVELOPER_ID_APPLICATION="$(signing_identity)"
	BUILD_ARGS+=(-sign "$DEVELOPER_ID_APPLICATION")
	if $NOTARIZE; then
		CONFIG+=("macos.notarize.keychainProfile=$(notary_profile)")
	else
		BUILD_ARGS+=(-skip-notarize)
	fi
fi
if $WIN; then
	check_nsis
	utf8_locale
	WINDOWS_CERTIFICATE="$(windows_certificate)"
	if [[ -n "$WINDOWS_CERTIFICATE" ]]; then
		CONFIG+=("windows.certificate=$WINDOWS_CERTIFICATE")
	else
		warn "LEAFLET_WINDOWS_CERTIFICATE is not set, so the Windows builds are unsigned"
	fi
fi
load_update_key
if $UPLOAD; then
	check_gh
	BUILD_ARGS+=(-upload)
fi

# The version, the changelog, notarization and the Windows certificate go
# into mygo.json for this build only: notarizing would fail the ad hoc
# debug builds, which read it too.
patch_config "${CONFIG[@]}"

info "building Leaflet $VERSION for ${PLATFORMS[*]}"
go tool mygo build -platform "$(
	IFS=,
	echo "${PLATFORMS[*]}"
)" -o "$OUT_DIR" ${BUILD_ARGS[@]+"${BUILD_ARGS[@]}"}

# MyGo names installers "leaflet-<version>-<arch>.dmg" ("leaflet-<version>.dmg"
# for universal) and "leaflet-setup-<version>-<arch>.exe", in a folder per
# platform; they are moved out beside those folders under the usual names.
if $MAC; then
	for arch in "${MAC_ARCHES[@]}"; do
		case "$arch" in
		universal) dmg="$OUT_DIR/darwin-universal/leaflet-$VERSION.dmg" ;;
		*) dmg="$OUT_DIR/darwin-$(goarch "$arch")/leaflet-$VERSION-$(goarch "$arch").dmg" ;;
		esac
		[[ -f "$dmg" ]] || die "expected $dmg after the build"
		if $NOTARIZE; then
			xcrun stapler validate "$dmg" >/dev/null || die "$dmg is not notarized"
		fi
		mv -f "$dmg" "$OUT_DIR/Leaflet-$VERSION-$arch.dmg"
		info "$OUT_DIR/Leaflet-$VERSION-$arch.dmg"
	done
fi
if $WIN; then
	for arch in "${WIN_ARCHES[@]}"; do
		setup="$OUT_DIR/windows-$(goarch "$arch")/leaflet-setup-$VERSION-$(goarch "$arch").exe"
		[[ -f "$setup" ]] || die "expected $setup after the build"
		mv -f "$setup" "$OUT_DIR/Leaflet-$VERSION-$arch-setup.exe"
		info "$OUT_DIR/Leaflet-$VERSION-$arch-setup.exe"
	done
fi
