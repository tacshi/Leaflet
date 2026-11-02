#!/usr/bin/env bash
# Builds the iOS app for the App Store and publishes it to TestFlight's
# internal group: the Go core (make ios-core), the Xcode project (make
# ios-project), an archive exported for App Store Connect, signed by the
# team scripts/install-ios.sh signs with, then asc, which uploads it, waits
# for Apple to process it and gives it to the group.
#
#   scripts/publish_testflight.sh 0.1.0 1
#
# The version and build are the IPA's only: ios/Config/Base.xcconfig is
# left as it is. Every upload needs a build number its version hasn't had.
#
# App Store Connect shows the icon of the builds uploaded, so the IPA is
# checked for it, as for the version, the build, the export-compliance
# answer and the privacy manifest, before anything is uploaded.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT_PATH="${ROOT_DIR}/ios/Leaflet.xcodeproj"
SCHEME="Leaflet"
BUNDLE_ID="app.capyvibe.leaflet"
# Leaflet's App Store Connect app, "Leaflet: Notes & Agents", and its
# internal group; the environment can point a release elsewhere.
APP_ID="${ASC_APP_ID:-6829665849}"
TESTFLIGHT_GROUP="${ASC_TESTFLIGHT_GROUP:-Leaflet Testers}"
TIMEOUT="${ASC_TESTFLIGHT_TIMEOUT:-45m}"
DERIVED_DATA="${ROOT_DIR}/ios/build/DerivedData"
ARCHIVE_PATH="${ROOT_DIR}/ios/build/Leaflet.xcarchive"
EXPORT_DIR="${ROOT_DIR}/ios/build/testflight"
# xcodebuild's output, kept here rather than shown.
BUILD_LOG="${ROOT_DIR}/ios/build/xcodebuild-testflight.log"

usage() {
  cat <<'EOF'
Usage: scripts/publish_testflight.sh <version> <build>

Build Leaflet for iOS and publish it to its internal TestFlight group. The
version looks like 1.2.3 and the build is a positive integer, one the
version hasn't had.

It needs a clean Git worktree, asc signed in to App Store Connect (asc auth
login), Xcode 27, XcodeGen and Go, and an Apple Distribution identity of the
team, or an Xcode account that can make one.

Environment:
  ASC_APP_ID        The App Store Connect app (default 6829665849, Leaflet)
  ASC_TESTFLIGHT_GROUP
                    The internal group to give the build to
                    (default "Leaflet Testers")
  ASC_TESTFLIGHT_TIMEOUT
                    How long to wait for the upload and Apple's processing
                    (default 45m)
  DEVELOPER_ID_APPLICATION
                    The Developer ID identity whose team signs the app, as
                    for scripts/install-ios.sh; unset, the keychain's one
                    Developer ID identity.
  DEVELOPMENT_TEAM  The team to sign with, over the Developer ID's.
  DEVELOPER_DIR     The Xcode to build with, over xcode-select's.
EOF
}

log() {
  printf '==> %s\n' "$*"
}

die() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "$1 not found in PATH."
}

source "${ROOT_DIR}/scripts/lib/ios.sh"

# Runs xcodebuild with its output in BUILD_LOG, showing only its errors if
# it fails.
run_xcodebuild() {
  if ! xcodebuild "$@" >>"${BUILD_LOG}" 2>&1; then
    local errors
    errors="$(grep -E 'error:' "${BUILD_LOG}" | sort -u | head -40 || true)"
    if [[ -n "${errors}" ]]; then
      printf '%s\n' "${errors}" >&2
    else
      tail -30 "${BUILD_LOG}" >&2
    fi
    die "xcodebuild failed. Its full output is in ${BUILD_LOG}."
  fi
}

# Prints the value at the key path $1 of the IPA's Info.plist, or nothing
# when it has none.
info_value() {
  plutil -extract "$1" raw -o - "${WORK_DIR}/Info.plist" 2>/dev/null || true
}

if [[ $# -eq 1 && ("$1" == -h || "$1" == --help) ]]; then
  usage
  exit 0
fi
[[ $# -eq 2 ]] || die "Usage: $0 <version> <build> (use --help)"
VERSION="$1"
BUILD_NUMBER="$2"
[[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
  die "The version must look like 1.2.3, not ${VERSION}."
[[ "${BUILD_NUMBER}" =~ ^[1-9][0-9]*$ ]] ||
  die "The build must be a positive integer, not ${BUILD_NUMBER}."

for tool in asc git xcodebuild make go xcodegen python3 unzip plutil security; do
  require_command "${tool}"
done

cd "${ROOT_DIR}"

# Untracked files count too: the project builds every Swift file under
# ios/Leaflet, committed or not.
[[ -z "$(git status --porcelain)" ]] ||
  die "The worktree isn't clean. Commit or stash the changes before publishing."

check_team

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/leaflet-testflight.XXXXXX")"
trap 'rm -rf "${WORK_DIR}"' EXIT

# Checked before building, which takes minutes: the app is Leaflet's, and
# the group is one of its internal ones.
log "Checking App Store Connect..."
asc auth doctor >/dev/null || die "asc isn't signed in to App Store Connect. Run asc auth doctor to see why."
asc apps view --id "${APP_ID}" --output json >"${WORK_DIR}/app.json"
asc testflight groups list --app "${APP_ID}" --internal --paginate --output json >"${WORK_DIR}/groups.json"
python3 -I - "${WORK_DIR}" "${BUNDLE_ID}" "${TESTFLIGHT_GROUP}" <<'CHECK_DESTINATION'
import json, sys
from pathlib import Path

root, bundle_id, group = Path(sys.argv[1]), sys.argv[2], sys.argv[3]
app = json.loads((root / "app.json").read_text())["data"]
if app["attributes"]["bundleId"] != bundle_id:
    sys.exit(f"Error: App Store Connect app {app['id']} is {app['attributes']['bundleId']}, not {bundle_id}. Set ASC_APP_ID to Leaflet's.")
groups = json.loads((root / "groups.json").read_text())["data"]
if not any(group in (g["id"], g["attributes"].get("name")) for g in groups):
    names = ", ".join(g["attributes"].get("name", g["id"]) for g in groups) or "none"
    sys.exit(f'Error: Leaflet has no internal TestFlight group "{group}" (it has: {names}). Create it, or set ASC_TESTFLIGHT_GROUP.')
CHECK_DESTINATION

log "Building the Go core..."
make -C "${ROOT_DIR}" ios-core

log "Generating the Xcode project..."
make -C "${ROOT_DIR}" ios-project

mkdir -p "$(dirname "${BUILD_LOG}")"
: >"${BUILD_LOG}"
rm -rf "${ARCHIVE_PATH}" "${EXPORT_DIR}"

# The version and build override Base.xcconfig's for this build only.
# -allowProvisioningUpdates lets automatic signing make the App Store
# profile, and the distribution certificate if the team has none.
settings=("MARKETING_VERSION=${VERSION}" "CURRENT_PROJECT_VERSION=${BUILD_NUMBER}")
[[ -n "${TEAM}" ]] && settings+=("DEVELOPMENT_TEAM=${TEAM}")
log "Archiving Leaflet ${VERSION} (${BUILD_NUMBER})..."
run_xcodebuild archive \
  -project "${PROJECT_PATH}" \
  -scheme "${SCHEME}" \
  -configuration Release \
  -destination "generic/platform=iOS" \
  -derivedDataPath "${DERIVED_DATA}" \
  -archivePath "${ARCHIVE_PATH}" \
  -onlyUsePackageVersionsFromResolvedFile \
  -allowProvisioningUpdates \
  "${settings[@]}"

# Exported as it was built: Xcode leaves the version and build alone.
EXPORT_OPTIONS="${WORK_DIR}/ExportOptions.plist"
plutil -create xml1 "${EXPORT_OPTIONS}"
plutil -insert method -string app-store-connect "${EXPORT_OPTIONS}"
plutil -insert destination -string export "${EXPORT_OPTIONS}"
plutil -insert signingStyle -string automatic "${EXPORT_OPTIONS}"
plutil -insert manageAppVersionAndBuildNumber -bool NO "${EXPORT_OPTIONS}"
plutil -insert uploadSymbols -bool YES "${EXPORT_OPTIONS}"
[[ -n "${TEAM}" ]] && plutil -insert teamID -string "${TEAM}" "${EXPORT_OPTIONS}"
log "Exporting the IPA..."
run_xcodebuild -exportArchive \
  -archivePath "${ARCHIVE_PATH}" \
  -exportPath "${EXPORT_DIR}" \
  -exportOptionsPlist "${EXPORT_OPTIONS}" \
  -allowProvisioningUpdates

shopt -s nullglob
IPAS=("${EXPORT_DIR}"/*.ipa)
[[ ${#IPAS[@]} -eq 1 ]] || die "Expected one IPA in ${EXPORT_DIR}, found ${#IPAS[@]}."
IPA="${IPAS[0]}"

# BSD awk wants the / escaped even in a bracket expression.
APP_DIR="$(unzip -Z1 "${IPA}" | awk '/^Payload\/[^\/]+\.app\/Info\.plist$/ { sub(/Info\.plist$/, ""); print; exit }')"
[[ -n "${APP_DIR}" ]] || die "No app Info.plist in ${IPA}."
unzip -p "${IPA}" "${APP_DIR}Info.plist" >"${WORK_DIR}/Info.plist"

[[ "$(info_value CFBundleIdentifier)" == "${BUNDLE_ID}" ]] ||
  die "The IPA is $(info_value CFBundleIdentifier), not ${BUNDLE_ID}."
[[ "$(info_value CFBundleShortVersionString)" == "${VERSION}" ]] ||
  die "The IPA's version is $(info_value CFBundleShortVersionString), not ${VERSION}."
[[ "$(info_value CFBundleVersion)" == "${BUILD_NUMBER}" ]] ||
  die "The IPA's build is $(info_value CFBundleVersion), not ${BUILD_NUMBER}."
[[ "$(info_value ITSAppUsesNonExemptEncryption)" == "false" ]] ||
  die "The IPA must say ITSAppUsesNonExemptEncryption = NO (ios/project.yml)."

# The icon: named in Info.plist for iPhone and iPad, in the compiled asset
# catalog, and the loose 120 and 152 point PNGs App Store Connect looks for.
for key in "CFBundleIcons.CFBundlePrimaryIcon.CFBundleIconName" "CFBundleIcons~ipad.CFBundlePrimaryIcon.CFBundleIconName"; do
  [[ "$(info_value "${key}")" == "AppIcon" ]] ||
    die "The IPA's Info.plist names no AppIcon at ${key}."
done
unzip -Z1 "${IPA}" >"${WORK_DIR}/files"
for icon in Assets.car AppIcon60x60@2x.png AppIcon76x76@2x~ipad.png; do
  grep -qxF "${APP_DIR}${icon}" "${WORK_DIR}/files" ||
    die "The IPA has no ${icon}, so it would have no icon."
done
# App Store Connect refuses an upload without the privacy manifest's
# reasons for the APIs the app uses.
grep -qxF "${APP_DIR}PrivacyInfo.xcprivacy" "${WORK_DIR}/files" ||
  die "The IPA has no PrivacyInfo.xcprivacy (ios/Leaflet/Resources)."
log "Built ${IPA#"${ROOT_DIR}"/}, with its icon."

log "Publishing ${VERSION} (${BUILD_NUMBER}) to ${TESTFLIGHT_GROUP}..."
asc publish testflight \
  --app "${APP_ID}" \
  --ipa "${IPA}" \
  --version "${VERSION}" \
  --build-number "${BUILD_NUMBER}" \
  --group "${TESTFLIGHT_GROUP}" \
  --wait \
  --timeout "${TIMEOUT}" \
  --output table

log "Published ${VERSION} (${BUILD_NUMBER}) to TestFlight."
