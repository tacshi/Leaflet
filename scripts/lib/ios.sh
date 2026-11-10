
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

# Dies unless HEAD is the commit origin has for the branch checked out, which
# must be main unless $1 is true.
check_published_commit() {
  local allow_branch="$1" branch remote
  branch="$(git branch --show-current)"
  [[ -n "${branch}" ]] || die "HEAD is detached, so no branch of origin has its commit. Check out main."
  [[ "${branch}" == main || "${allow_branch}" == true ]] ||
    die "TestFlight builds are cut from main, and this is ${branch} (--allow-branch to override)."
  remote="$(git ls-remote origin "refs/heads/${branch}")" || die "Could not reach origin."
  [[ "${remote%%[[:space:]]*}" == "$(git rev-parse HEAD)" ]] ||
    die "HEAD is not what origin/${branch} is. Push or pull first, so the build is of a published commit."
}
