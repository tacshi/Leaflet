#!/usr/bin/env bash
# Publishes a Leaflet release to GitHub, where installed copies find their
# updates: the installers, update archives and manifests that
# scripts/build-release.sh builds, for macOS unless --win says otherwise.
#
#   scripts/publish.sh 0.2.0                 # build, review the notes, publish
#   scripts/publish.sh 0.2.0 --mac --win     # for macOS and Windows
#   scripts/publish.sh 0.2.0 --win           # for Windows only
#   scripts/publish.sh 0.2.0 --dry-run       # check, show the notes and the steps
#   scripts/publish.sh 0.2.0 --no-upload     # build into build/release only
#   scripts/publish.sh 0.2.0 --allow-branch  # release from a branch, not main
#
# The notes are the "## <version>" section of CHANGELOG.md, used verbatim
# when it has one. Otherwise they are drafted from the subjects of the
# commits since the last v* tag and shown for review: [y]es publishes them,
# [e]dit opens them in $EDITOR and shows them again, anything else aborts.
#
# A dry run makes the checks of a real one and reports every one that fails,
# not just the first, then shows the notes and the commands it would run. It
# builds, tags, pushes and uploads nothing, and leaves build/release alone.
#
# Signing, notarization, NSIS and the update signing key are as in
# scripts/build-release.sh. The committed mygo.json and CHANGELOG.md are
# never edited: the build gets the version and the notes for the run only.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/release.sh"
cd "$ROOT_DIR"

VERSION=""
MAC=false
WIN=false
DRY_RUN=false
NO_UPLOAD=false
ALLOW_BRANCH=false
for arg in "$@"; do
	case "$arg" in
	--mac) MAC=true ;;
	--win) WIN=true ;;
	--dry-run | -n) DRY_RUN=true ;;
	--no-upload) NO_UPLOAD=true ;;
	--allow-branch) ALLOW_BRANCH=true ;;
	-h | --help)
		sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	-*) die "unknown option: $arg" ;;
	*)
		[[ -z "$VERSION" ]] || die "one version, please: $VERSION or $arg"
		VERSION="${arg#v}"
		;;
	esac
done
[[ -n "$VERSION" ]] || die "usage: scripts/publish.sh <version> [--mac] [--win] [--dry-run] [--no-upload] [--allow-branch]"
check_version "$VERSION"
$MAC || $WIN || MAC=true
[[ "$(uname -s)" == "Darwin" ]] || die "Leaflet releases are built on macOS"
TOOLS=(go git python3)
if $MAC; then TOOLS+=(xcrun security); fi
for tool in "${TOOLS[@]}"; do
	command -v "$tool" >/dev/null 2>&1 || die "needs $tool"
done
PLATFORM_ARGS=()
if $MAC; then PLATFORM_ARGS+=(--mac); fi
if $WIN; then PLATFORM_ARGS+=(--win); fi

TAG="v$VERSION"
REPO="$(config_value updates.github)"
STAGE_DIR="$ROOT_DIR/build/release"
# A dry run drafts its notes in a folder of its own, not in build/release.
if $DRY_RUN; then
	WORK_DIR="$(mktemp -d)"
	trap 'rm -rf "$WORK_DIR"' EXIT
else
	WORK_DIR="$STAGE_DIR"
fi

PROBLEMS=0
# problem stops a run; a dry run reports it and goes on, to find them all.
problem() {
	$DRY_RUN || die "$*"
	printf 'problem: %s\n' "$*" >&2
	PROBLEMS=$((PROBLEMS + 1))
}

# check runs a check of lib/release.sh, which dies when it fails, and has
# its failure be a problem.
check() {
	if ! $DRY_RUN; then
		"$@" >/dev/null
		return
	fi
	local err
	err="$({ "$@" >/dev/null; } 2>&1)" || problem "${err#"$(basename "$0"): "}"
}

# run runs a step of the release, which a dry run only shows.
run() {
	if $DRY_RUN; then
		local arg
		printf '  would run:'
		for arg in "$@"; do printf ' %q' "${arg#"$ROOT_DIR/"}"; done
		printf '\n'
	else
		"$@"
	fi
}

# ------------------------------------------------------------------- checks

BRANCH="$(git branch --show-current)"
info "Leaflet $VERSION for ${PLATFORM_ARGS[*]#--} from ${BRANCH:-a detached HEAD} at $(git rev-parse --short HEAD), as $TAG of $REPO"

# What the build needs, checked before the notes are reviewed.
if $MAC; then
	check signing_identity
	check notary_profile
fi
if $WIN; then
	check check_nsis
	check windows_certificate
	[[ -n "${LEAFLET_WINDOWS_CERTIFICATE:-}" ]] || warn "LEAFLET_WINDOWS_CERTIFICATE is not set, so the Windows builds will be unsigned"
fi
check load_update_key
$NO_UPLOAD || check check_gh

if [[ "$BRANCH" != "main" ]] && ! $ALLOW_BRANCH; then
	problem "releases are cut from main, and this is ${BRANCH:-a detached HEAD} (--allow-branch to override)"
fi
[[ -z "$(git status --porcelain)" ]] || problem "the working tree has changes; commit or stash them first"
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
	problem "the tag $TAG already exists"
fi
if ! $NO_UPLOAD; then
	if ! ORIGIN="$(git remote get-url origin 2>/dev/null)"; then
		problem "there is no git remote origin to push $TAG to; add github.com/$REPO as origin"
	else
		case "$ORIGIN" in
		*github.com[:/]"$REPO" | *github.com[:/]"$REPO".git | *github.com[:/]"$REPO"/) ;;
		*) problem "origin is $ORIGIN, not github.com/$REPO, where mygo.json has installed copies look for updates" ;;
		esac
		if [[ -z "$BRANCH" ]]; then
			problem "HEAD is detached, so no branch of origin has the commit $TAG would name"
		elif ! REMOTE_REFS="$(git ls-remote origin "refs/heads/$BRANCH" "refs/tags/$TAG")"; then
			problem "could not reach origin, $ORIGIN"
		else
			[[ "$(awk -v ref="refs/heads/$BRANCH" '$2 == ref { print $1 }' <<<"$REMOTE_REFS")" == "$(git rev-parse HEAD)" ]] ||
				problem "HEAD is not what origin/$BRANCH is; push or pull first, so the tag names a published commit"
			if awk -v ref="refs/tags/$TAG" '$2 == ref { found = 1 } END { exit !found }' <<<"$REMOTE_REFS"; then
				problem "the tag $TAG already exists on origin"
			fi
		fi
	fi
	if [[ "$(gh release view "$TAG" --repo "$REPO" --json isDraft --jq .isDraft 2>/dev/null)" == "false" ]]; then
		problem "the release $TAG of $REPO is already published"
	fi
fi

# ------------------------------------------------------------------- notes

# These notes are PUBLISHED, to the release page of a public repository, and
# shown by the update prompt of every installed copy, so drafts from commit
# subjects are always reviewed before an upload.
NOTES_FILE="$WORK_DIR/notes.md"
$DRY_RUN || rm -rf "$STAGE_DIR"
mkdir -p "$WORK_DIR"

# changelog_notes prints the section of CHANGELOG.md under "## <version>" (or
# "## [<version>]"), as MyGo reads it, or nothing.
changelog_notes() {
	[[ -f "$ROOT_DIR/CHANGELOG.md" ]] || return 0
	python3 -I - "$ROOT_DIR/CHANGELOG.md" "$VERSION" <<'EOF'
import sys
path, version = sys.argv[1:]
notes, inside = [], False
with open(path, encoding="utf-8") as f:
    lines = f.read().replace("\r\n", "\n").split("\n")
for line in lines:
    if line.startswith("## "):
        if inside:
            break
        fields = line[3:].split()
        heading = fields[0] if fields else ""
        if heading.startswith("["):
            heading = heading[1:].split("]", 1)[0]
        inside = heading.removeprefix("v") == version
        continue
    if inside:
        notes.append(line)
print("\n".join(notes).strip())
EOF
}

has_notes() { grep -qE '[^[:space:]]' "$NOTES_FILE"; }

show_notes() {
	printf '\n--- release notes for %s ---\n' "$VERSION"
	if has_notes; then cat "$NOTES_FILE"; else printf '(none)\n'; fi
	printf -- '---\n'
}

changelog_notes >"$NOTES_FILE"
if has_notes; then
	info "notes:    the $VERSION section of CHANGELOG.md"
	if $DRY_RUN; then show_notes; fi
else
	PREV_TAG="$(git describe --tags --abbrev=0 --match 'v*' 2>/dev/null || true)"
	if [[ -n "$PREV_TAG" ]]; then
		LOG_ARGS=("$PREV_TAG..HEAD")
		info "notes:    drafted from $PREV_TAG..HEAD (CHANGELOG.md has no $VERSION section)"
	else
		# The first release: capped, so the whole history doesn't become notes.
		LOG_ARGS=(-n 30 HEAD)
		info "notes:    drafted from the last 30 commits (no previous tag, and CHANGELOG.md has no $VERSION section)"
	fi
	git log --no-merges --pretty=tformat:'- %s' "${LOG_ARGS[@]}" |
		grep -viE '^- (wip([^[:alnum:]]|$)|fixup!|squash!|amend!)' >"$NOTES_FILE" || true

	KEEP_DRAFT="To keep the notes, add them to CHANGELOG.md under \"## $VERSION\" and commit
it, then run again: that section is used verbatim."
	if $DRY_RUN; then
		show_notes
		$NO_UPLOAD || info "a real run asks to publish these notes, [y]es / [e]dit / [N]o"
	elif ! $NO_UPLOAD && [[ ! -t 0 ]]; then
		die "drafted release notes need an interactive review before upload.
$KEEP_DRAFT"
	elif ! $NO_UPLOAD; then
		while :; do
			show_notes
			printf '\nPublish these notes? [y]es / [e]dit / [N]o: '
			read -r reply
			case "$reply" in
			y | Y) break ;;
			e | E) "${VISUAL:-${EDITOR:-vi}}" "$NOTES_FILE" ;;
			*) die "aborted. The draft is $NOTES_FILE.
$KEEP_DRAFT" ;;
			esac
		done
	fi
fi
# MyGo fails a build whose changelog has no notes for its version.
has_notes || problem "the notes for $VERSION are empty; write them in CHANGELOG.md under \"## $VERSION\""

# ------------------------------------------------------------------- build

# MyGo takes the "## <version>" section of a changelog as the notes, and
# uploads to a draft release that it creates.
printf '## %s\n\n%s\n' "$VERSION" "$(cat "$NOTES_FILE")" >"$WORK_DIR/CHANGELOG.md"
BUILD_ARGS=("$VERSION" "${PLATFORM_ARGS[@]}" -o "$STAGE_DIR/out" --changelog "$STAGE_DIR/CHANGELOG.md")
$NO_UPLOAD || BUILD_ARGS+=(--upload)

$DRY_RUN || info "building Leaflet $VERSION at $(git rev-parse --short HEAD)"
run "$ROOT_DIR/scripts/build-release.sh" "${BUILD_ARGS[@]}"

# ------------------------------------------------------------------- publish

# The assets are on the draft before the release is published, so installed
# copies never read a manifest whose archive is missing.
if ! $NO_UPLOAD; then
	run git tag -a "$TAG" -F "$STAGE_DIR/notes.md"
	run git push origin "$TAG"
	run gh release edit "$TAG" --repo "$REPO" --draft=false --latest --title "Leaflet $VERSION" --notes-file "$STAGE_DIR/notes.md"
fi

if $DRY_RUN; then
	((PROBLEMS == 0)) || die "dry run: $PROBLEMS problem(s) above would stop this release"
	info "dry run: the checks pass; nothing was built, tagged or published"
elif $NO_UPLOAD; then
	info "built into build/release/out; nothing was published"
else
	info "published https://github.com/$REPO/releases/tag/$TAG"
fi
