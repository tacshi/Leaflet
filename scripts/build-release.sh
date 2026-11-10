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
#   scripts/build-release.sh 0.2.0 --upload   # and upload to a draft GitHub release
#   scripts/build-release.sh --changelog f.md # notes from f.md, not CHANGELOG.md
#   scripts/build-release.sh --allow-unsigned # let --upload take unsigned or unnotarized builds
#
# The installers go to build/release: Leaflet-<version>-{arm64,x64,universal}.dmg
# for macOS (Apple silicon, Intel, both) and Leaflet-<version>-{x64,arm64}-setup.exe
# for Windows, beside a folder per platform with the app and its updates.
# --arch picks among each platform's: universal is macOS only, so
# --arch universal alone builds macOS only.
#
# --upload, once every installer is built and every disk image notarized,
# puts the installers and updates in the draft release v<version> of
# tacshi/Leaflet, with gh, before the installers are renamed: there they
# keep MyGo's names, leaflet-<version>-<arch>.dmg (leaflet-<version>.dmg
# for universal) and leaflet-setup-<version>-<arch>.exe, by which
# SHA256SUMS lists them and install.ps1 finds its installer. Installed
# copies see the update once the draft is published. A release that is
# published already is refused: its files are what installed copies took.
# So is a draft of another commit, which the draft records as it is made: a
# release tags one commit. It needs the version named, as mygo.json's is no
# release's.
#
# Signs updates with MYGO_UPDATER_PRIVATE_KEY, else the key in the file
# LEAFLET_UPDATE_KEY, by default
#   ~/Library/Application Support/mygo/update-keys/Leaflet/mygo-update.key
# made with
#   go tool mygo keygen -o ~/Library/Application\ Support/mygo/update-keys/Leaflet
# Its public half is updates.publicKey in mygo.json.
#
# macOS: needs Xcode 26 or later, whose macOS 26 SDK builds leaflet-speech,
# the on-device speech helper (scripts/build-speech.sh). Signs with
# DEVELOPER_ID_APPLICATION, or the keychain's one Developer ID Application
# identity when it is not set, and notarizes with a notarytool keychain
# profile: LEAFLET_NOTARY_PROFILE, "Leaflet" by default, made with
#   xcrun notarytool store-credentials Leaflet --apple-id <id> --team-id <team>
#
# Windows: needs NSIS (brew install makensis) for the installers. Signs with
# the .pfx LEAFLET_WINDOWS_CERTIFICATE names, whose password is
# MYGO_WINDOWS_CERTIFICATE_PASSWORD, using osslsigncode
# (scripts/sign-windows.sh), and fails if any program of the build comes out
# unsigned; without it the builds are unsigned and SmartScreen warns about
# them.
#
# --upload refuses unsigned Windows builds, and macOS ones --skip-notarize
# leaves unnotarized, which installed copies would update to, unless
# --allow-unsigned, or LEAFLET_ALLOW_UNSIGNED=1, says to upload them anyway.
#
# Paths given, -o, --changelog and those in LEAFLET_WINDOWS_CERTIFICATE and
# LEAFLET_UPDATE_KEY, are from where this is run.
#
# To build a server in, set MYGO_LDFLAGS as README.md's Build and release
# shows.
# scripts/publish.sh runs this with --changelog, -o and --upload, and
# --allow-unsigned when it is given it.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/release.sh"
cd "$ROOT_DIR"

VERSION=""
MAC=false
WIN=false
ARCHES=()
NOTARIZE=true
UPLOAD=false
ALLOW_UNSIGNED=false
if [[ "${LEAFLET_ALLOW_UNSIGNED:-}" == 1 ]]; then ALLOW_UNSIGNED=true; fi
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
	--allow-unsigned) ALLOW_UNSIGNED=true ;;
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
		sed -n '/^set -euo pipefail/q;2,$p' "$0" | sed 's/^# \{0,1\}//'
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
if $UPLOAD && [[ -z "$VERSION" ]]; then
	die "--upload needs the version to release, as in scripts/build-release.sh 0.2.0 --upload"
fi
[[ -n "$VERSION" ]] || VERSION="$(config_value version)"
check_version "$VERSION"
OUT_DIR="$(abs_path "$OUT_DIR")"
if [[ -n "$CHANGELOG" ]]; then
	CHANGELOG="$(abs_path "$CHANGELOG")"
	[[ -f "$CHANGELOG" ]] || die "no changelog $CHANGELOG"
	CHANGELOG="$(cd "$(dirname "$CHANGELOG")" && pwd)/$(basename "$CHANGELOG")"
fi

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
	check_speech
	DEVELOPER_ID_APPLICATION="$(signing_identity)"
	BUILD_ARGS+=(-sign "$DEVELOPER_ID_APPLICATION")
	if $NOTARIZE; then
		CONFIG+=("macos.notarize.keychainProfile=$(notary_profile)")
	else
		if $UPLOAD && ! $ALLOW_UNSIGNED; then
			die "--upload with --skip-notarize would publish macOS builds that are not notarized; add --allow-unsigned (or LEAFLET_ALLOW_UNSIGNED=1) to upload them anyway"
		fi
		BUILD_ARGS+=(-skip-notarize)
	fi
fi
if $WIN; then
	check_nsis
	utf8_locale
	WINDOWS_CERTIFICATE="$(windows_certificate)"
	if [[ -n "$WINDOWS_CERTIFICATE" ]]; then
		export LEAFLET_WINDOWS_CERTIFICATE="$WINDOWS_CERTIFICATE"
		CONFIG+=("windows.signCommand=scripts/sign-windows.sh %1")
	elif $UPLOAD && ! $ALLOW_UNSIGNED; then
		die "LEAFLET_WINDOWS_CERTIFICATE is not set, so --upload would publish unsigned Windows builds; set it, or add --allow-unsigned (or LEAFLET_ALLOW_UNSIGNED=1) to upload them anyway"
	else
		warn "LEAFLET_WINDOWS_CERTIFICATE is not set, so the Windows builds are unsigned"
	fi
fi
UPDATE_KEY="$(update_key)"
[[ -z "$UPDATE_KEY" ]] || CONFIG+=("updates.privateKey=$UPDATE_KEY")
if $UPLOAD; then
	check_gh
	check_draft "$VERSION"
	COMMIT="$(git rev-parse HEAD)"
	check_pushed "$COMMIT"
	check_draft_commit "$VERSION" "$COMMIT"
fi

patch_config "${CONFIG[@]}"

if $MAC; then
	SPEECH_ARCHES=()
	for arch in "${MAC_ARCHES[@]}"; do
		case "$arch" in
		universal) SPEECH_ARCHES+=(arm64 x64) ;;
		*) SPEECH_ARCHES+=("$arch") ;;
		esac
	done
	"$ROOT_DIR/scripts/build-speech.sh" "${SPEECH_ARCHES[@]}"
fi

if $WIN; then
	for arch in "${WIN_ARCHES[@]}"; do build_cli "$(config_value name)" "$VERSION" "$(goarch "$arch")"; done
fi

info "building Leaflet $VERSION for ${PLATFORMS[*]}"
go tool mygo build -platform "$(
	IFS=,
	echo "${PLATFORMS[*]}"
)" -o "$OUT_DIR" ${BUILD_ARGS[@]+"${BUILD_ARGS[@]}"}

BUILT=()
NAMES=()
DIRS=()
if $MAC; then
	for arch in "${MAC_ARCHES[@]}"; do
		case "$arch" in
		universal) dir=darwin-universal dmg="leaflet-$VERSION.dmg" ;;
		*) dir="darwin-$(goarch "$arch")" dmg="leaflet-$VERSION-$(goarch "$arch").dmg" ;;
		esac
		[[ -f "$OUT_DIR/$dir/$dmg" ]] || die "expected $OUT_DIR/$dir/$dmg after the build"
		if $NOTARIZE; then
			xcrun stapler validate "$OUT_DIR/$dir/$dmg" >/dev/null || die "$OUT_DIR/$dir/$dmg is not notarized"
		fi
		BUILT+=("$OUT_DIR/$dir/$dmg")
		NAMES+=("Leaflet-$VERSION-$arch.dmg")
		DIRS+=("$dir")
	done
fi
if $WIN; then
	for arch in "${WIN_ARCHES[@]}"; do
		dir="windows-$(goarch "$arch")"
		setup="$OUT_DIR/$dir/leaflet-setup-$VERSION-$(goarch "$arch").exe"
		[[ -f "$setup" ]] || die "expected $setup after the build"
		if [[ -n "$WINDOWS_CERTIFICATE" ]]; then
			check_signed "$OUT_DIR/$dir"/*.exe "$OUT_DIR/$dir"/bin/*.exe
		fi
		BUILT+=("$setup")
		NAMES+=("Leaflet-$VERSION-$arch-setup.exe")
		DIRS+=("$dir")
	done
fi

if $UPLOAD; then
	NOTES="$(mktemp)"
	changelog_notes "${CHANGELOG:-$ROOT_DIR/CHANGELOG.md}" "$VERSION" >"$NOTES"
	upload_release "$VERSION" "$OUT_DIR" "$NOTES" "$COMMIT" "${DIRS[@]}"
	rm -f "$NOTES"
	info "uploaded to the draft release v$VERSION of $(config_value updates.github); publish it once every platform is there"
fi

for i in "${!BUILT[@]}"; do
	mv -f "${BUILT[$i]}" "$OUT_DIR/${NAMES[$i]}"
	info "$OUT_DIR/${NAMES[$i]}"
done
