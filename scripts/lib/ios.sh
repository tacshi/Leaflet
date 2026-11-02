# Shared by the iOS app's scripts, install-ios.sh and publish_testflight.sh;
# source it. Its functions use the log and die the script defines.

# The iOS app is signed by the team TEAM names: DEVELOPMENT_TEAM, or the
# team of the Developer ID the desktop's release builds sign with
# (scripts/lib/release.sh), "Developer ID Application: Name (TEAMID)":
# DEVELOPER_ID_APPLICATION, or the keychain's one Developer ID identity.
# Without either, the team Local.xcconfig, which git ignores, sets for this
# Mac, which leaves TEAM empty.
TEAM=""
check_team() {
  local identity="${DEVELOPER_ID_APPLICATION:-}" found
  if [[ -z "${identity}" ]]; then
    found="$(security find-identity -v -p codesigning 2>/dev/null |
      sed -n 's/.*"\(Developer ID Application: [^"]*\)".*/\1/p' | sort -u)"
    [[ "$(printf '%s' "${found}" | grep -c .)" -eq 1 ]] && identity="${found}"
  fi
  TEAM="${DEVELOPMENT_TEAM:-}"
  if [[ -z "${TEAM}" && "${identity}" =~ \(([A-Z0-9]{10})\)$ ]]; then
    TEAM="${BASH_REMATCH[1]}"
  fi
  if [[ -n "${TEAM}" ]]; then
    log "Signing with team ${TEAM}."
    return
  fi
  local local_config="${ROOT_DIR}/ios/Config/Local.xcconfig"
  if [[ -f "${local_config}" ]] && grep -Eq '^[[:space:]]*DEVELOPMENT_TEAM[[:space:]]*=[[:space:]]*[A-Z0-9]+' "${local_config}"; then
    return
  fi
  die "No Apple team to sign with. Set DEVELOPER_ID_APPLICATION or DEVELOPMENT_TEAM, or add DEVELOPMENT_TEAM = <team ID> to ios/Config/Local.xcconfig."
}
