#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT_PATH="${ROOT_DIR}/ios/Leaflet.xcodeproj"
SCHEME="Leaflet"
DERIVED_DATA="${ROOT_DIR}/ios/build/DerivedData"
DESTINATION_TIMEOUT_SECONDS="${DESTINATION_TIMEOUT_SECONDS:-20}"
BUILD_LOG="${ROOT_DIR}/ios/build/xcodebuild.log"

BUILD_TYPE="release"
SKIP_BUILD=0
SKIP_CORE=0
CLEAN_BUILD=0
LAUNCH=0
VERBOSE=0
ALL_DEVICES=0
APP_PATH=""

REQUESTED_UDIDS=()
SELECTED_UDIDS=()
SELECTED_NAMES=()

usage() {
  cat <<'EOF'
Usage: scripts/install-ios.sh [options]

Build Leaflet for iOS and install it on connected iPhones and iPads.

Options:
  --release       Build and install the release app (default)
  --debug         Build and install Leaflet Debug, beside the release app
  --clean         Remove Xcode's build folder first
  --skip-core     Reuse ios/build/LeafletCore.xcframework instead of
                  rebuilding the Go core
  --skip-build    Install an existing .app without building
  --app PATH      Install a specific .app bundle
  --device UDID   Target a specific device. Repeat the flag to install on
                  several in one run
  --all           Target every connected device
  --launch        Launch the app after installing it
  --verbose       Show xcodebuild's output as it builds, rather than only
                  its errors, with the rest in ios/build/xcodebuild.log
  -h, --help      Show this help message

With no --device/--all, the script lists every paired device and asks which to
use, accepting several: "1,3", "1 3", "a" for all, or Enter for whichever are
connected. Devices devicectl cannot reach are listed too, marked
[not reachable]: they can still be picked, and preflight says why if one does
not work. The app is built once and installed on each target in turn, so each
has to be in the signing profile the build used.

Environment:
  DEVELOPER_ID_APPLICATION
                    The Developer ID identity whose team signs the app, as
                    the desktop's release builds use it; unset, the
                    keychain's one Developer ID identity.
  DEVELOPMENT_TEAM  The team to sign with, over the Developer ID's.
  DEVELOPER_DIR     The Xcode to build with, over xcode-select's.

Examples:
  scripts/install-ios.sh
  scripts/install-ios.sh --launch
  scripts/install-ios.sh --all --launch
  scripts/install-ios.sh --device UDID-A --device UDID-B
  scripts/install-ios.sh --debug --launch
  scripts/install-ios.sh --skip-build --app ios/build/DerivedData/Build/Products/Release-iphoneos/Leaflet.app
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

xcode_configuration() {
  if [[ "${BUILD_TYPE}" == "debug" ]]; then
    echo "Debug"
  else
    echo "Release"
  fi
}

source "${ROOT_DIR}/scripts/lib/ios.sh"

discover_devices() {
  local devices_json
  devices_json="$(mktemp)"

  log "Discovering iOS devices..."
  if ! xcrun devicectl list devices \
    --timeout "${DESTINATION_TIMEOUT_SECONDS}" \
    --json-output "${devices_json}" >/dev/null 2>&1; then
    rm -f "${devices_json}"
    die "Unable to enumerate connected iOS devices."
  fi

  device_names=()
  device_udids=()
  device_reachable=()

  local line reachable udid name
  while IFS= read -r line; do
    [[ -n "${line}" ]] || continue
    reachable="${line%%$'\t'*}"
    line="${line#*$'\t'}"
    udid="${line%%$'\t'*}"
    name="${line#*$'\t'}"
    device_udids+=("${udid}")
    device_names+=("${name}")
    device_reachable+=("${reachable}")
  done < <(
    python3 - "${devices_json}" <<'DEVICES_JSON'
import json, sys

with open(sys.argv[1]) as handle:
    devices = json.load(handle)["result"]["devices"]

rows = []
for device in devices:
    hardware = device.get("hardwareProperties", {})
    properties = device.get("deviceProperties", {})
    connection = device.get("connectionProperties", {})
    if hardware.get("reality") == "simulated":
        continue
    if hardware.get("platform") != "iOS":
        continue
    identifier = hardware.get("udid") or device.get("identifier")
    name = properties.get("name")
    if not identifier or not name:
        continue
    reachable = (
        properties.get("bootState") == "booted"
        or connection.get("transportType") is not None
    )
    kind = hardware.get("deviceType") or "iOS"
    version = properties.get("osVersionNumber")
    label = name + " (" + kind + (", iOS " + version if version else "") + ")"
    rows.append((0 if reachable else 1, name.lower(), reachable, identifier, label))

for _, _, reachable, identifier, label in sorted(rows):
    print(("1" if reachable else "0") + "\t" + identifier + "\t" + label)
DEVICES_JSON
  )

  rm -f "${devices_json}"

  ((${#device_udids[@]} > 0)) || die "No physical iOS devices are paired with this Mac."
}

select_index() {
  local index="$1" udid="${device_udids[$1]}" existing
  for existing in ${SELECTED_UDIDS[@]+"${SELECTED_UDIDS[@]}"}; do
    [[ "${existing}" == "${udid}" ]] && return 0
  done
  SELECTED_UDIDS+=("${udid}")
  SELECTED_NAMES+=("${device_names[$index]}")
}

index_of_udid() {
  local i
  for i in "${!device_udids[@]}"; do
    if [[ "${device_udids[$i]}" == "$1" ]]; then
      echo "$i"
      return 0
    fi
  done
  return 1
}

select_all_devices() {
  local i
  for i in "${!device_udids[@]}"; do
    select_index "$i"
  done
}

select_reachable_devices() {
  local i
  for i in "${!device_udids[@]}"; do
    [[ "${device_reachable[$i]}" == "1" ]] && select_index "$i"
  done
  ((${#SELECTED_UDIDS[@]} > 0))
}

parse_selection() {
  local line="$1" token
  local -a picked=()

  if [[ -z "${line// /}" ]]; then
    select_reachable_devices && return 0
    echo "No device is reachable now: pick one by number to try it anyway."
    return 1
  fi

  if [[ "${line}" == "a" || "${line}" == "all" ]]; then
    select_all_devices
    return 0
  fi

  for token in ${line//,/ }; do
    [[ "${token}" =~ ^[0-9]+$ ]] || return 1
    ((token >= 1 && token <= ${#device_udids[@]})) || return 1
    picked+=("$((token - 1))")
  done

  ((${#picked[@]} > 0)) || return 1
  for token in "${picked[@]}"; do
    select_index "${token}"
  done
}

select_devices() {
  discover_devices

  if ((${#REQUESTED_UDIDS[@]} > 0)); then
    local requested index
    for requested in "${REQUESTED_UDIDS[@]}"; do
      index="$(index_of_udid "${requested}")" ||
        die "Device '${requested}' is not paired with this Mac."
      select_index "${index}"
    done
  elif ((ALL_DEVICES == 1)); then
    select_reachable_devices ||
      die "No device is reachable now. Pass --device UDID to try one anyway."
  elif [[ ! -t 0 ]]; then
    select_reachable_devices ||
      die "No device is reachable now. Pass --device UDID (repeatable) or --all."
  else
    echo "Devices:"
    local i suffix
    for i in "${!device_udids[@]}"; do
      suffix=""
      [[ "${device_reachable[$i]}" == "1" ]] || suffix="   [not reachable]"
      printf '  %d) %s — %s%s\n' \
        "$((i + 1))" "${device_names[$i]}" "${device_udids[$i]}" "${suffix}"
    done

    local selection
    while true; do
      printf 'Select [1-%d, comma separated, "a" for all, Enter for connected]: ' \
        "${#device_udids[@]}"
      read -r selection
      parse_selection "${selection}" && break
      [[ -n "${selection// /}" ]] && echo "Invalid selection."
    done
  fi

  local i
  for i in "${!SELECTED_UDIDS[@]}"; do
    log "Target device: ${SELECTED_NAMES[$i]} — ${SELECTED_UDIDS[$i]}"
  done
}

preflight_device() {
  local udid="$1" name="$2" preflight_output
  preflight_output="$(mktemp)"

  log "Preparing developer services on ${name}..."
  if ! xcrun devicectl device info ddiServices \
    --device "${udid}" \
    --timeout "${DESTINATION_TIMEOUT_SECONDS}" >"${preflight_output}" 2>&1; then
    cat "${preflight_output}" >&2
    if grep -q "Developer Mode is disabled" "${preflight_output}"; then
      printf 'Error: Developer Mode is disabled on %s. Turn it on in Settings > Privacy & Security > Developer Mode, unlock the device, then run this again.\n' \
        "${name}" >&2
    else
      printf 'Error: Unable to prepare developer services on %s.\n' "${name}" >&2
    fi
    rm -f "${preflight_output}"
    return 1
  fi

  rm -f "${preflight_output}"
}

preflight_devices() {
  local -a ready_udids=() ready_names=()
  local i
  for i in "${!SELECTED_UDIDS[@]}"; do
    if preflight_device "${SELECTED_UDIDS[$i]}" "${SELECTED_NAMES[$i]}"; then
      ready_udids+=("${SELECTED_UDIDS[$i]}")
      ready_names+=("${SELECTED_NAMES[$i]}")
    else
      log "Skipping ${SELECTED_NAMES[$i]}."
    fi
  done

  ((${#ready_udids[@]} > 0)) || die "No target device is ready to install on."

  SELECTED_UDIDS=(${ready_udids[@]+"${ready_udids[@]}"})
  SELECTED_NAMES=(${ready_names[@]+"${ready_names[@]}"})
}

build_ios() {
  local configuration
  configuration="$(xcode_configuration)"

  if [[ "${SKIP_CORE}" -eq 1 && -d "${ROOT_DIR}/ios/build/LeafletCore.xcframework" ]]; then
    log "Reusing the Go core in ios/build/LeafletCore.xcframework."
  else
    log "Building the Go core..."
    quietly make -C "${ROOT_DIR}" ios-core
  fi

  log "Generating the Xcode project..."
  quietly make -C "${ROOT_DIR}" ios-project

  if [[ "${CLEAN_BUILD}" -eq 1 ]]; then
    log "Removing Xcode's build folder..."
    rm -rf "${DERIVED_DATA}"
  fi

  local -a settings=()
  [[ -n "${TEAM}" ]] && settings+=("DEVELOPMENT_TEAM=${TEAM}")
  local -a build=(
    xcodebuild build
    -project "${PROJECT_PATH}"
    -scheme "${SCHEME}"
    -configuration "${configuration}"
    -destination "id=${SELECTED_UDIDS[0]}"
    -derivedDataPath "${DERIVED_DATA}"
    -allowProvisioningUpdates
    ${settings[@]+"${settings[@]}"}
  )
  log "Building ${configuration} for ${SELECTED_NAMES[0]}..."
  mkdir -p "$(dirname "${BUILD_LOG}")"
  if [[ "${VERBOSE}" -eq 1 ]]; then
    "${build[@]}" 2>&1 | tee "${BUILD_LOG}"
    return
  fi
  if ! "${build[@]}" >"${BUILD_LOG}" 2>&1; then
    local errors
    errors="$(grep -E 'error:' "${BUILD_LOG}" | sort -u | head -40 || true)"
    if [[ -n "${errors}" ]]; then
      printf '%s\n' "${errors}" >&2
    else
      tail -30 "${BUILD_LOG}" >&2
    fi
    die "The build failed. Its full output is in ${BUILD_LOG}."
  fi
  log "Built."
}

quietly() {
  if [[ "${VERBOSE}" -eq 1 ]]; then
    "$@"
    return
  fi
  local output
  if ! output="$("$@" 2>&1)"; then
    printf '%s\n' "${output}" >&2
    return 1
  fi
}

default_app_path() {
  echo "${DERIVED_DATA}/Build/Products/$(xcode_configuration)-iphoneos/Leaflet.app"
}

bundle_identifier() {
  local plist="${APP_PATH}/Info.plist"
  [[ -f "${plist}" ]] || die "Info.plist not found in ${APP_PATH}."

  /usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "${plist}"
}

install_app() {
  local udid="$1" name="$2"

  log "Installing ${APP_PATH} on ${name}..."
  quietly xcrun devicectl device install app --device "${udid}" "${APP_PATH}" || return 1
  log "Installed on ${name}."
}

launch_app() {
  local udid="$1" name="$2" bundle_id
  bundle_id="$(bundle_identifier)"

  log "Launching ${bundle_id} on ${name}..."
  quietly xcrun devicectl device process launch --device "${udid}" "${bundle_id}" || return 1
}

install_all() {
  [[ -d "${APP_PATH}" ]] || die ".app bundle not found: ${APP_PATH}"

  local -a failed=()
  local i
  for i in "${!SELECTED_UDIDS[@]}"; do
    if ! install_app "${SELECTED_UDIDS[$i]}" "${SELECTED_NAMES[$i]}"; then
      failed+=("${SELECTED_NAMES[$i]}")
      continue
    fi
    if [[ "${LAUNCH}" -eq 1 ]]; then
      launch_app "${SELECTED_UDIDS[$i]}" "${SELECTED_NAMES[$i]}" ||
        failed+=("${SELECTED_NAMES[$i]} (launch)")
    fi
  done

  if ((${#failed[@]} > 0)); then
    printf 'Error: failed on %d of %d devices: %s\n' \
      "${#failed[@]}" "${#SELECTED_UDIDS[@]}" "$(printf '%s, ' "${failed[@]}" | sed 's/, $//')" >&2
    exit 1
  fi

  if ((${#SELECTED_UDIDS[@]} > 1)); then
    log "Done: ${#SELECTED_UDIDS[@]} devices."
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --release)
      BUILD_TYPE="release"
      ;;
    --debug)
      BUILD_TYPE="debug"
      ;;
    --clean)
      CLEAN_BUILD=1
      ;;
    --skip-core)
      SKIP_CORE=1
      ;;
    --skip-build)
      SKIP_BUILD=1
      ;;
    --app)
      [[ $# -ge 2 ]] || die "--app requires a path"
      APP_PATH="$2"
      SKIP_BUILD=1
      shift
      ;;
    --device)
      [[ $# -ge 2 ]] || die "--device requires a UDID"
      REQUESTED_UDIDS+=("$2")
      shift
      ;;
    --all)
      ALL_DEVICES=1
      ;;
    --launch)
      LAUNCH=1
      ;;
    --verbose)
      VERBOSE=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "Unknown option: $1 (use --help)"
      ;;
  esac
  shift
done

require_command xcrun
require_command python3
if [[ "${SKIP_BUILD}" -eq 0 ]]; then
  require_command xcodebuild
  require_command make
  require_command go
  require_command xcodegen
  check_team
fi

select_devices
preflight_devices

if [[ "${SKIP_BUILD}" -eq 0 ]]; then
  build_ios
fi

if [[ -z "${APP_PATH}" ]]; then
  APP_PATH="$(default_app_path)"
fi

[[ -d "${APP_PATH}" ]] || die "Built app not found at ${APP_PATH}. Remove --skip-build to build first."

install_all
