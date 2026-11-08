# Shared by the build and publish scripts; source it.

die() {
	printf '%s: %s\n' "$(basename "$0")" "$*" >&2
	exit 1
}
info() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# The directory the script was run from, which the paths it is given are
# relative to, though it then works in ROOT_DIR: publish.sh's, for the
# build-release.sh it runs from ROOT_DIR, which exports it.
CALLER_DIR="${LEAFLET_CALLER_DIR:-$PWD}"
export LEAFLET_CALLER_DIR="$CALLER_DIR"

# abs_path prints the path $1 in full: as it is when it is, else from the
# directory the script was run from.
abs_path() {
	case "$1" in
	/*) printf '%s\n' "$1" ;;
	*) printf '%s\n' "$CALLER_DIR/$1" ;;
	esac
}

# speech_toolchain fails, printing why, unless this Mac can build
# leaflet-speech (scripts/build-speech.sh): swiftc and the macOS 26 SDK or
# later, whose SpeechTranscriber it uses, from Xcode 26 or its command line
# tools. xcrun is there without them too, as a stub that offers to install
# them, so it asks for the SDK.
speech_toolchain() {
	local sdk
	if ! sdk="$(xcrun --sdk macosx --show-sdk-version 2>/dev/null)" || ! xcrun --find swiftc >/dev/null 2>&1; then
		echo "it needs Xcode 26 or later, or its command line tools (xcode-select --install)"
		return 1
	fi
	if ((${sdk%%.*} < 26)); then
		echo "it needs the macOS 26 SDK or later, for SpeechTranscriber, from Xcode 26 or later; this one is macOS $sdk"
		return 1
	fi
}

# check_speech fails unless this Mac can build leaflet-speech, which the
# Mac apps ship, saying why.
check_speech() {
	local why
	why="$(speech_toolchain)" || die "the Mac apps ship leaflet-speech, which can't be built here: $why"
}

# check_version fails unless $1 looks like 1.2.3.
check_version() {
	[[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "the version must look like 1.2.3, not $1"
}

# version_after succeeds when the version $1 comes after $2, both 1.2.3.
version_after() {
	local a b i
	IFS=. read -r -a a <<<"$1"
	IFS=. read -r -a b <<<"$2"
	for i in 0 1 2; do
		if ((10#${a[i]} != 10#${b[i]})); then
			((10#${a[i]} > 10#${b[i]}))
			return
		fi
	done
	return 1
}

# CONFIG_LOCK names the build of this checkout that has mygo.json changed,
# by its process, while it does; CONFIG_KEPT keeps the committed file
# meanwhile, which it puts back as it ends. CONFIG_RECOVER is held while a
# build puts back what one that is gone left changed.
CONFIG_LOCK="$ROOT_DIR/build/mygo.json.lock"
CONFIG_KEPT="$ROOT_DIR/build/mygo.json.committed"
CONFIG_RECOVER="$ROOT_DIR/build/mygo.json.recover"
CONFIG_HELD=false

# read_config prints the value in the mygo.json $1 at the dotted keys $2.
read_config() {
	python3 -I -c '
import json, sys
value = json.load(open(sys.argv[1]))
for key in sys.argv[2].split("."):
    value = value[key]
print(value)' "$1" "$2"
}

# config_value prints the value in mygo.json at the dotted keys $1, as in
# "version" or "updates.github": as this build has it changed, or as it is
# committed while another build has it changed.
config_value() {
	if ! $CONFIG_HELD && [[ -f "$CONFIG_KEPT" ]] && read_config "$CONFIG_KEPT" "$1" 2>/dev/null; then
		return
	fi
	read_config "$ROOT_DIR/mygo.json" "$1"
}

# hold_config waits until no other build of this checkout has mygo.json
# changed, and then holds it for this one, with the committed file kept in
# CONFIG_KEPT, until the script exits: builds that overlapped would read
# each other's changes, and one could put back another's as the committed
# file. What a build that is gone, killed, left changed is put back first.
hold_config() {
	local holder said=false
	mkdir -p "$ROOT_DIR/build"
	until (
		set -o noclobber
		printf '%s\n' "$$" >"$CONFIG_LOCK"
	) 2>/dev/null; do
		holder="$(cat "$CONFIG_LOCK" 2>/dev/null || true)"
		if [[ -n "$holder" ]] && ! kill -0 "$holder" 2>/dev/null && recover_config "$holder"; then
			continue
		fi
		if ! $said; then
			info "waiting for another build of this checkout (process ${holder:-?}), which has mygo.json changed; $CONFIG_LOCK names it"
			said=true
		fi
		sleep 1
	done
	CONFIG_HELD=true
	trap release_config EXIT
	# Kept whole or not at all: a build killed as it copies has nothing
	# changed yet to put back.
	cp -p "$ROOT_DIR/mygo.json" "$CONFIG_KEPT.$$"
	mv -f "$CONFIG_KEPT.$$" "$CONFIG_KEPT"
}

# release_config puts the committed mygo.json back, and lets another build
# change it.
release_config() {
	if [[ -f "$CONFIG_KEPT" ]]; then mv -f "$CONFIG_KEPT" "$ROOT_DIR/mygo.json"; fi
	rm -f "$CONFIG_LOCK"
	CONFIG_HELD=false
}

# recover_config puts back the committed mygo.json that the build $1, by
# its process, which is gone, left changed, and lets go of its hold. One
# build at a time does, and only while that build still holds it; it fails
# when another is at it.
recover_config() {
	(
		set -o noclobber
		: >"$CONFIG_RECOVER"
	) 2>/dev/null || return 1
	if [[ "$(cat "$CONFIG_LOCK" 2>/dev/null || true)" == "$1" ]]; then
		if [[ -f "$CONFIG_KEPT" ]]; then
			warn "a build that ended early left mygo.json changed; putting the committed one back"
			mv -f "$CONFIG_KEPT" "$ROOT_DIR/mygo.json"
		fi
		rm -f "$CONFIG_LOCK"
	fi
	rm -f "$CONFIG_RECOVER"
}

# patch_config changes mygo.json for this run only: each argument is
# <dotted key>=<value>, which sets the value, or -<dotted key>, which
# removes the key. The first call holds mygo.json for this build
# (hold_config), and the committed file is put back when the script exits.
patch_config() {
	$CONFIG_HELD || hold_config
	python3 -I - "$ROOT_DIR/mygo.json" "$@" <<'EOF'
import json, sys
path, pairs = sys.argv[1], sys.argv[2:]
with open(path) as f:
    config = json.load(f)
for pair in pairs:
    remove = pair.startswith("-")
    keys, _, value = pair.removeprefix("-").partition("=")
    keys = keys.split(".")
    node = config
    for key in keys[:-1]:
        node = node.setdefault(key, {})
    if remove:
        node.pop(keys[-1], None)
    else:
        node[keys[-1]] = value
with open(path, "w") as f:
    json.dump(config, f, indent=2)
    f.write("\n")
EOF
}

# build_cli builds the command line for the Windows app named $1, version
# $2, on Go's architecture $3 (cmd/leaflet), into what the app ships as
# bin/<command>.exe: the app is a GUI program there, which a terminal
# neither waits for nor shows the output of. Its name is the app's, in
# lower case with dashes, as cli.CommandName has it: leaflet, or
# leaflet-debug beside it. MyGo signs it with the app.
build_cli() {
	local name="$1" version="$2" goarch="$3" bin cmd
	bin="$ROOT_DIR/resources/windows-$goarch/bin"
	cmd="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]' | awk '{$1=$1; gsub(/ /, "-")} 1')"
	# The other build's command must not ship in this one.
	rm -rf "$bin"
	info "building $cmd.exe for windows/$goarch"
	GOOS=windows GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X 'main.appName=$name' -X 'main.version=$version' ${MYGO_LDFLAGS:-}" \
		-o "$bin/$cmd.exe" ./cmd/leaflet
}

# signing_identity prints DEVELOPER_ID_APPLICATION, or the keychain's one
# Developer ID Application identity when it is not set, and fails unless the
# keychain has it.
signing_identity() {
	local identities found id="${DEVELOPER_ID_APPLICATION:-}"
	identities="$(security find-identity -v -p codesigning)"
	if [[ -z "$id" ]]; then
		found="$(printf '%s\n' "$identities" | sed -n 's/.*"\(Developer ID Application: [^"]*\)".*/\1/p' | sort -u)"
		[[ -n "$found" ]] || die "no Developer ID Application identity in the keychain; set DEVELOPER_ID_APPLICATION"
		[[ "$(printf '%s\n' "$found" | wc -l)" -eq 1 ]] ||
			die "several Developer ID identities; set DEVELOPER_ID_APPLICATION to one of: $(printf '%s\n' "$found" | paste -sd ';' - | sed 's/;/; /g')"
		id="$found"
	fi
	printf '%s\n' "$identities" | grep -qF "\"$id\"" || die "no signing identity \"$id\" in the keychain"
	printf '%s\n' "$id"
}

# notary_profile prints the notarytool keychain profile, LEAFLET_NOTARY_PROFILE
# or "Leaflet", and fails unless it works.
notary_profile() {
	local profile="${LEAFLET_NOTARY_PROFILE:-Leaflet}"
	xcrun notarytool history --keychain-profile "$profile" >/dev/null 2>&1 ||
		die "the notarytool keychain profile \"$profile\" doesn't work; make it with xcrun notarytool store-credentials"
	printf '%s\n' "$profile"
}

# windows_certificate prints LEAFLET_WINDOWS_CERTIFICATE, the .pfx that signs
# Windows builds, or nothing when it is not set, and fails when it is set but
# cannot sign: the file is missing, or osslsigncode, which signs on macOS, is.
windows_certificate() {
	local cert="${LEAFLET_WINDOWS_CERTIFICATE:-}"
	[[ -n "$cert" ]] || return 0
	cert="$(abs_path "$cert")"
	[[ -f "$cert" ]] || die "no Windows certificate at $cert (LEAFLET_WINDOWS_CERTIFICATE)"
	command -v osslsigncode >/dev/null 2>&1 || die "signing Windows builds on macOS needs osslsigncode (brew install osslsigncode)"
	[[ -n "${MYGO_WINDOWS_CERTIFICATE_PASSWORD:-}" ]] || warn "MYGO_WINDOWS_CERTIFICATE_PASSWORD is not set; the certificate must have no password"
	printf '%s\n' "$(cd "$(dirname "$cert")" && pwd)/$(basename "$cert")"
}

# check_signed fails unless each of the Windows programs $@ carries a
# signature, by the certificate table of its PE header: a certificate that
# signed some of a build should leave none of it to go out unsigned.
check_signed() {
	python3 -I - "$@" <<'EOF' || die "a Windows certificate was given, so every program of the build should be signed"
import struct, sys

def signed(path):
    try:
        with open(path, "rb") as f:
            data = f.read(4096)
        pe = struct.unpack_from("<I", data, 0x3c)[0]
        directories = {0x10b: 96, 0x20b: 112}[struct.unpack_from("<H", data, pe + 24)[0]]
        # The fifth directory, its size after its offset.
        return struct.unpack_from("<I", data, pe + 24 + directories + 4 * 8 + 4)[0] != 0
    except (OSError, KeyError, struct.error):
        return False

sys.exit("\n".join(f"not signed: {path}" for path in sys.argv[1:] if not signed(path)) or None)
EOF
}

# check_nsis fails unless makensis is there to make Windows installers.
check_nsis() {
	command -v makensis >/dev/null 2>&1 || die "Windows installers need NSIS (brew install makensis)"
}

# utf8_locale gives makensis the UTF-8 locale it needs: Homebrew's 3.13
# aborts with std::bad_alloc under the C locale, which a shell without
# LANG has.
utf8_locale() {
	[[ "$(locale charmap 2>/dev/null)" == "UTF-8" ]] && return
	export LC_ALL=en_US.UTF-8 LANG=en_US.UTF-8
}

# update_key prints the file of the key that signs updates, the one
# LEAFLET_UPDATE_KEY names (by default the one `go tool mygo keygen` wrote for
# Leaflet), for updates.privateKey: read there, the key stays out of what
# every program the release runs inherits, as $EDITOR and git's hooks. It
# prints nothing when MYGO_UPDATER_PRIVATE_KEY, which MyGo takes first, is
# set, and fails when there is neither.
update_key() {
	[[ -z "${MYGO_UPDATER_PRIVATE_KEY:-}" ]] || return 0
	local file
	file="$(abs_path "${LEAFLET_UPDATE_KEY:-$HOME/Library/Application Support/mygo/update-keys/Leaflet/mygo-update.key}")"
	[[ -f "$file" ]] || die "no update signing key at $file; set LEAFLET_UPDATE_KEY or MYGO_UPDATER_PRIVATE_KEY"
	printf '%s\n' "$file"
}

# check_gh fails unless the GitHub CLI is there to upload with, signed in.
check_gh() {
	command -v gh >/dev/null 2>&1 || die "uploads need the GitHub CLI, gh"
	gh auth status >/dev/null 2>&1 || [[ -n "${GH_TOKEN:-}${GITHUB_TOKEN:-}" ]] || die "uploads need gh auth login, or GH_TOKEN"
}

# check_draft fails when the GitHub release v$1 of updates.github is
# published: an upload to it would replace, under the same version, the
# files installed copies have taken. A draft, or no release yet, passes.
check_draft() {
	local repo draft
	repo="$(config_value updates.github)"
	if draft="$(gh release view "v$1" --repo "$repo" --json isDraft --jq .isDraft 2>/dev/null)" && [[ "$draft" != true ]]; then
		die "the release v$1 of $repo is already published; a new build needs a new version"
	fi
}

# check_draft_commit fails when the draft release v$1 of updates.github holds
# a build of another commit than $2, which upload_release records on it as
# it makes it (--target): a release tags one commit, and a build of another
# would go out under it. No release yet passes.
check_draft_commit() {
	local repo target
	repo="$(config_value updates.github)"
	if target="$(gh release view "v$1" --repo "$repo" --json targetCommitish --jq .targetCommitish 2>/dev/null)" && [[ "$target" != "$2" ]]; then
		die "the draft release v$1 of $repo holds a build of $target, not of $2; delete the draft and run again"
	fi
}

# check_pushed fails unless updates.github has the commit $1, which a draft
# can record only then: found out before the long builds, not after them.
check_pushed() {
	local repo
	repo="$(config_value updates.github)"
	gh api "repos/$repo/commits/$1" --silent >/dev/null 2>&1 ||
		die "$repo does not have the commit $1, which its draft release would record; push it first"
}

# update_manifests prints the update manifest of every platform of a release.
# Installed copies read their platform's from the latest release, and find
# no update in one without it.
update_manifests() {
	printf 'update-%s.json\n' darwin-arm64 darwin-amd64 darwin-universal windows-amd64 windows-arm64
}

# missing_manifests prints those of update_manifests that are not among the
# names, a line each, in $1.
missing_manifests() {
	local name
	while read -r name; do
		grep -qxF "$name" <<<"$1" || printf '%s\n' "$name"
	done < <(update_manifests)
}

# changelog_notes prints the section of the changelog $1 under
# "## <version>" (or "## [<version>]") for the version $2, as MyGo reads
# it, or nothing.
changelog_notes() {
	[[ -f "$1" ]] || return 0
	python3 -I - "$1" "$2" <<'EOF'
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

# merge_checksums adds to the SHA256SUMS $1 the lines of the SHA256SUMS $2,
# an earlier upload's to the same release, for the files $1 doesn't list,
# keeping the lines sorted by name, as MyGo writes them.
merge_checksums() {
	python3 -I - "$1" "$2" <<'EOF'
import re, sys
line = re.compile(r"^([0-9a-f]{64}) [ *](.+)$")
def parse(path):
    with open(path, encoding="utf-8") as f:
        return {m[2]: m[1] for m in (line.match(l.rstrip("\r")) for l in f.read().split("\n")) if m}
path, earlier = sys.argv[1:]
sums = parse(path)
for name, sum in parse(earlier).items():
    sums.setdefault(name, sum)
with open(path, "w", encoding="utf-8") as f:
    f.writelines(f"{sums[name]}  {name}\n" for name in sorted(sums))
EOF
}

# upload_release puts what go tool mygo build wrote in the output directory
# $2, built from the commit $4, for the platform folders $5…, on the GitHub
# release v$1 of updates.github, with gh, as mygo build -upload does: the
# files the build's SHA256SUMS lists, which MyGo writes for the files it
# would upload (the installers, update archives, deltas and install
# scripts), and SHA256SUMS, first, then the update manifests, so that none
# ever names a file not there yet. They keep MyGo's names, by which
# SHA256SUMS lists them and install.ps1 finds its installer. A release there
# already must be a draft of that commit, and keeps the checksums of the
# files other builds put on it; else it is made, as a draft of it, with the
# notes in the file $3.
upload_release() {
	local version="$1" out="$2" notes="$3" commit="$4" repo tag dir f sum name found assets earlier
	shift 4
	local files=() manifests=()
	repo="$(config_value updates.github)"
	tag="v$version"
	[[ -f "$out/SHA256SUMS" ]] || die "no SHA256SUMS in $out, which lists what the build made to upload"
	# By MyGo's own list, a kind of file it starts to make goes up too,
	# rather than being left out. One install script serves every
	# architecture of a system, so the first folder's goes.
	while read -r sum name; do
		[[ -n "$sum" ]] || continue
		name="${name#\*}"
		found=""
		for dir in "$@"; do
			if [[ -f "$out/$dir/$name" ]]; then
				found="$out/$dir/$name"
				break
			fi
		done
		[[ -n "$found" ]] || die "SHA256SUMS lists $name, which is in none of the build's folders ($*)"
		files+=("$found")
	done <"$out/SHA256SUMS"
	for dir in "$@"; do
		for f in "$out/$dir"/update-*.json; do
			if [[ -f "$f" ]]; then manifests+=("$f"); fi
		done
	done
	((${#files[@]} + ${#manifests[@]} > 0)) || die "nothing to upload in $out"
	check_draft "$version"
	check_draft_commit "$version" "$commit"
	if gh release view "$tag" --repo "$repo" >/dev/null 2>&1; then
		assets="$(gh release view "$tag" --repo "$repo" --json assets --jq '.assets[].name')"
		if grep -qx SHA256SUMS <<<"$assets"; then
			earlier="$(mktemp -d)"
			gh release download "$tag" --repo "$repo" --pattern SHA256SUMS --dir "$earlier"
			merge_checksums "$out/SHA256SUMS" "$earlier/SHA256SUMS"
			rm -rf "$earlier"
		fi
	else
		info "creating the draft release $tag of $repo"
		gh release create "$tag" --repo "$repo" --draft --target "$commit" --title "$tag" --notes-file "$notes" >/dev/null
	fi
	files+=("$out/SHA256SUMS")
	info "uploading ${#files[@]} files, then ${#manifests[@]} manifests, to $tag of $repo"
	if ((${#files[@]} > 0)); then gh release upload "$tag" --repo "$repo" --clobber "${files[@]}"; fi
	if ((${#manifests[@]} > 0)); then gh release upload "$tag" --repo "$repo" --clobber "${manifests[@]}"; fi
}
