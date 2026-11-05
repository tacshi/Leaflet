#!/usr/bin/env bash
# Publishes a Leaflet release to GitHub, where installed copies find their
# updates: the installers, update archives and manifests that
# scripts/build-release.sh builds, for macOS and Windows unless --mac or
# --win picks one.
#
#   scripts/publish.sh 0.2.0                 # build, review the notes, publish
#   scripts/publish.sh 0.2.0 --mac           # for macOS only
#   scripts/publish.sh 0.2.0 --win           # for Windows only
#   scripts/publish.sh 0.2.0 --dry-run       # check, show the notes and the steps
#   scripts/publish.sh 0.2.0 --no-upload     # build into build/release only
#   scripts/publish.sh 0.2.0 --allow-branch  # release from a branch, not main
#   scripts/publish.sh 0.2.0 --allow-unsigned # publish unsigned Windows builds
#   scripts/publish.sh 0.2.0 --mac --allow-partial # publish one platform alone
#
# A release needs the update manifest of every platform: installed copies
# read theirs from the latest release, and find no update in one without it,
# and a published release takes no more builds. --mac or --win alone so stops
# with its build on the draft, tagging nothing; running this for the other
# platform, from the same commit, adds that build and publishes both.
# --allow-partial publishes without the others, whose installed copies then
# wait for the next release.
#
# A draft records the commit it was made from, and a run from any other is
# refused, before it builds and again before it publishes: the tag would
# name that commit, over a build of another. Delete a draft left by a run
# given up, and run again.
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
# A release tags the commit it was built from, which must still be checked
# out, unchanged, once the build is done, and is marked the latest: its
# version must come after every v* tag's, here and on origin.
#
# Signing, notarization, NSIS and the update signing key are as in
# scripts/build-release.sh. Without LEAFLET_WINDOWS_CERTIFICATE the Windows
# builds would be unsigned, which a release refuses unless --allow-unsigned,
# or LEAFLET_ALLOW_UNSIGNED=1, says to publish them anyway. The committed
# mygo.json and CHANGELOG.md are never edited: the build gets the version
# and the notes for the run only.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/release.sh"
cd "$ROOT_DIR"

VERSION=""
MAC=false
WIN=false
DRY_RUN=false
NO_UPLOAD=false
ALLOW_BRANCH=false
ALLOW_PARTIAL=false
ALLOW_UNSIGNED=false
if [[ "${LEAFLET_ALLOW_UNSIGNED:-}" == 1 ]]; then ALLOW_UNSIGNED=true; fi
for arg in "$@"; do
	case "$arg" in
	--mac) MAC=true ;;
	--win) WIN=true ;;
	--dry-run | -n) DRY_RUN=true ;;
	--no-upload) NO_UPLOAD=true ;;
	--allow-branch) ALLOW_BRANCH=true ;;
	--allow-partial) ALLOW_PARTIAL=true ;;
	--allow-unsigned) ALLOW_UNSIGNED=true ;;
	-h | --help)
		sed -n '/^set -euo pipefail/q;2,$p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	-*) die "unknown option: $arg" ;;
	*)
		[[ -z "$VERSION" ]] || die "one version, please: $VERSION or $arg"
		VERSION="${arg#v}"
		;;
	esac
done
[[ -n "$VERSION" ]] || die "usage: scripts/publish.sh <version> [--mac] [--win] [--dry-run] [--no-upload] [--allow-branch] [--allow-partial] [--allow-unsigned]"
check_version "$VERSION"
# Both, unless one is asked for.
if ! $MAC && ! $WIN; then
	MAC=true
	WIN=true
fi
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

# check_manifests stops a release without the update manifest of every
# platform, $1 naming what it will hold, a line each.
check_manifests() {
	local missing
	missing="$(missing_manifests "$1" | paste -sd ' ' - | sed 's/ /, /g')"
	[[ -n "$missing" ]] || return 0
	if $ALLOW_PARTIAL; then
		warn "$TAG has no $missing, so installed copies of those platforms will find no update until the next release"
	else
		problem "$TAG would have no $missing, so installed copies of those platforms would find no update; run this for the other platform, which adds its build to the draft, or add --allow-partial to publish without it"
	fi
}

# ------------------------------------------------------------------- checks

BRANCH="$(git branch --show-current)"
# The commit the release is built from, and tagged.
COMMIT="$(git rev-parse HEAD)"
SHORT="$(git rev-parse --short HEAD)"
info "Leaflet $VERSION for ${PLATFORM_ARGS[*]#--} from ${BRANCH:-a detached HEAD} at $SHORT, as $TAG of $REPO"

# What the build needs, checked before the notes are reviewed.
if $MAC; then
	check check_speech
	check signing_identity
	check notary_profile
fi
if $WIN; then
	check check_nsis
	check windows_certificate
	if [[ -z "${LEAFLET_WINDOWS_CERTIFICATE:-}" ]]; then
		if ! $NO_UPLOAD && ! $ALLOW_UNSIGNED; then
			problem "LEAFLET_WINDOWS_CERTIFICATE is not set, so the Windows builds would be published unsigned; set it, or add --allow-unsigned (or LEAFLET_ALLOW_UNSIGNED=1) to publish them anyway"
		else
			warn "LEAFLET_WINDOWS_CERTIFICATE is not set, so the Windows builds will be unsigned"
		fi
	fi
fi
check update_key
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
		elif ! REMOTE_REFS="$(git ls-remote origin "refs/heads/$BRANCH" "refs/tags/v*")"; then
			problem "could not reach origin, $ORIGIN"
		else
			[[ "$(awk -v ref="refs/heads/$BRANCH" '$2 == ref { print $1 }' <<<"$REMOTE_REFS")" == "$COMMIT" ]] ||
				problem "HEAD is not what origin/$BRANCH is; push or pull first, so the tag names a published commit"
			if awk -v ref="refs/tags/$TAG" '$2 == ref { found = 1 } END { exit !found }' <<<"$REMOTE_REFS"; then
				problem "the tag $TAG already exists on origin"
			fi
		fi
	fi
	# The release is marked the latest, which an older version must not be.
	LATEST=""
	while read -r tag; do
		if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] && { [[ -z "$LATEST" ]] || version_after "${tag#v}" "${LATEST#v}"; }; then
			LATEST="$tag"
		fi
	done < <(
		git tag --list 'v*'
		awk '{ sub("^refs/tags/", "", $2); print $2 }' <<<"${REMOTE_REFS:-}"
	)
	if [[ -n "$LATEST" ]] && ! version_after "$VERSION" "${LATEST#v}"; then
		problem "$VERSION does not come after $LATEST, the latest release, and a release is marked the latest"
	fi
	check check_draft "$VERSION"
	check check_draft_commit "$VERSION" "$COMMIT"
fi

# ------------------------------------------------------------------- notes

# These notes are PUBLISHED, to the release page of a public repository, and
# shown by the update prompt of every installed copy, so drafts from commit
# subjects are always reviewed before an upload.
NOTES_FILE="$WORK_DIR/notes.md"
$DRY_RUN || rm -rf "$STAGE_DIR"
mkdir -p "$WORK_DIR"

has_notes() { grep -qE '[^[:space:]]' "$NOTES_FILE"; }

show_notes() {
	printf '\n--- release notes for %s ---\n' "$VERSION"
	if has_notes; then cat "$NOTES_FILE"; else printf '(none)\n'; fi
	printf -- '---\n'
}

changelog_notes "$ROOT_DIR/CHANGELOG.md" "$VERSION" >"$NOTES_FILE"
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

# MyGo, and build-release.sh's draft release, take the "## <version>"
# section of a changelog as the notes.
printf '## %s\n\n%s\n' "$VERSION" "$(cat "$NOTES_FILE")" >"$WORK_DIR/CHANGELOG.md"
BUILD_ARGS=("$VERSION" "${PLATFORM_ARGS[@]}" -o "$STAGE_DIR/out" --changelog "$STAGE_DIR/CHANGELOG.md")
$NO_UPLOAD || BUILD_ARGS+=(--upload)
if $ALLOW_UNSIGNED; then BUILD_ARGS+=(--allow-unsigned); fi

$DRY_RUN || info "building Leaflet $VERSION at $SHORT"
run "$ROOT_DIR/scripts/build-release.sh" "${BUILD_ARGS[@]}"

# ------------------------------------------------------------------- publish

# The assets are on the draft before the release is published, so installed
# copies never read a manifest whose archive is missing.
if ! $NO_UPLOAD; then
	# The build took minutes: what it built must still be what is checked
	# out, or the tag would name another commit than the one built.
	if ! $DRY_RUN && { [[ "$(git rev-parse HEAD)" != "$COMMIT" ]] || [[ -n "$(git status --porcelain --untracked-files=no)" ]]; }; then
		die "the checkout changed while Leaflet $VERSION was built from $SHORT, so which commit the draft release $TAG holds a build of is not known; delete the draft and run again"
	fi
	if $DRY_RUN; then
		# What a draft from an earlier run has, and what this one would add.
		ASSETS="$(gh release view "$TAG" --repo "$REPO" --json assets --jq '.assets[].name' 2>/dev/null || true)"
		if $MAC; then ASSETS+=$'\n'"$(update_manifests | grep darwin)"; fi
		if $WIN; then ASSETS+=$'\n'"$(update_manifests | grep windows)"; fi
	else
		check_draft_commit "$VERSION" "$COMMIT"
		ASSETS="$(gh release view "$TAG" --repo "$REPO" --json assets --jq '.assets[].name')"
	fi
	check_manifests "$ASSETS"
	run git tag -a "$TAG" -F "$STAGE_DIR/notes.md" "$COMMIT"
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
