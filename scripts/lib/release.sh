# shellcheck shell=bash

die() {
	printf '%s: %s\n' "$(basename "$0")" "$*" >&2
	exit 1
}
info() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CALLER_DIR="${LEAFLET_CALLER_DIR:-$PWD}"
export LEAFLET_CALLER_DIR="$CALLER_DIR"

abs_path() {
	case "$1" in
	/*) printf '%s\n' "$1" ;;
	*) printf '%s\n' "$CALLER_DIR/$1" ;;
	esac
}

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

check_speech() {
	local why
	why="$(speech_toolchain)" || die "the Mac apps ship leaflet-speech, which can't be built here: $why"
}

check_version() {
	[[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "the version must look like 1.2.3, not $1"
}

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

# A build that changes mygo.json for its run keeps a copy of the committed
# one, and of what it wrote, to put the committed one back as it ends, and
# a lock, so that one build of the checkout at a time changes it. They are
# in the checkout's git folder, which make clean leaves alone (outside a
# git checkout, in build/).
CONFIG_DIR="$(git -C "$ROOT_DIR" rev-parse --absolute-git-dir 2>/dev/null)/leaflet-build" ||
	CONFIG_DIR="$ROOT_DIR/build"
CONFIG_LOCK="$CONFIG_DIR/mygo.json.lock"
CONFIG_KEPT="$CONFIG_DIR/mygo.json.committed"
CONFIG_WRITTEN="$CONFIG_DIR/mygo.json.written"
CONFIG_RECOVER="$CONFIG_DIR/mygo.json.recover"
CONFIG_HELD=false

read_config() {
	python3 -I -c '
import json, sys
value = json.load(open(sys.argv[1]))
for key in sys.argv[2].split("."):
    value = value[key]
print(value)' "$1" "$2"
}

config_value() {
	if ! $CONFIG_HELD && [[ -f "$CONFIG_KEPT" ]] && read_config "$CONFIG_KEPT" "$1" 2>/dev/null; then
		return
	fi
	read_config "$ROOT_DIR/mygo.json" "$1"
}

hold_config() {
	local holder said=false
	mkdir -p "$CONFIG_DIR"
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
	cp -p "$ROOT_DIR/mygo.json" "$CONFIG_KEPT.$$"
	mv -f "$CONFIG_KEPT.$$" "$CONFIG_KEPT"
	cp -p "$CONFIG_KEPT" "$CONFIG_WRITTEN"
}

release_config() {
	restore_config
	rm -f "$CONFIG_LOCK"
	CONFIG_HELD=false
}

# Puts the committed mygo.json that hold_config kept back, over what
# patch_config wrote, saying $1 if it is given and they differ. A mygo.json
# changed since, by hand or by another session, stays as it is, with the
# committed one beside it to compare it with.
restore_config() {
	local config="$ROOT_DIR/mygo.json"
	if [[ ! -f "$CONFIG_KEPT" ]]; then
		if [[ -f "$CONFIG_WRITTEN" ]]; then
			warn "the copy of the committed mygo.json kept to put back, $CONFIG_KEPT, is gone, so mygo.json may still have the build's changes; git diff mygo.json shows them"
		fi
	elif [[ ! -f "$config" ]] || cmp -s "$config" "$CONFIG_KEPT" || cmp -s "$config" "$CONFIG_WRITTEN"; then
		if [[ -n "${1:-}" ]] && ! cmp -s "$config" "$CONFIG_KEPT"; then warn "$1"; fi
		mv -f "$CONFIG_KEPT" "$config"
	else
		mv -f "$CONFIG_KEPT" "$config.committed"
		warn "mygo.json changed while a build had it changed, so it stays as it is; the committed one is beside it, mygo.json.committed, to compare it with"
	fi
	rm -f "$CONFIG_WRITTEN"
}

recover_config() {
	(
		set -o noclobber
		: >"$CONFIG_RECOVER"
	) 2>/dev/null || return 1
	if [[ "$(cat "$CONFIG_LOCK" 2>/dev/null || true)" == "$1" ]]; then
		restore_config "a build that ended early left mygo.json changed; putting the committed one back"
		rm -f "$CONFIG_LOCK"
	fi
	rm -f "$CONFIG_RECOVER"
}

# Sets mygo.json's keys for the build's run, each key.sub=value, or removes
# those given as -key.sub. What it writes goes to CONFIG_WRITTEN first, so
# that mygo.json never holds it unrecorded.
patch_config() {
	$CONFIG_HELD || hold_config
	python3 -I - "$ROOT_DIR/mygo.json" "$CONFIG_WRITTEN" "$@" <<'EOF'
import json, sys
path, written, pairs = sys.argv[1], sys.argv[2], sys.argv[3:]
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
text = json.dumps(config, indent=2) + "\n"
for out in (written, path):
    with open(out, "w") as f:
        f.write(text)
EOF
}

build_cli() {
	local name="$1" version="$2" goarch="$3" bin cmd
	bin="$ROOT_DIR/resources/windows-$goarch/bin"
	cmd="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]' | awk '{$1=$1; gsub(/ /, "-")} 1')"
	rm -rf "$bin"
	info "building $cmd.exe for windows/$goarch"
	GOOS=windows GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X 'main.appName=$name' -X 'main.version=$version' ${MYGO_LDFLAGS:-}" \
		-o "$bin/$cmd.exe" ./cmd/leaflet
}

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

notary_profile() {
	local profile="${LEAFLET_NOTARY_PROFILE:-Leaflet}"
	xcrun notarytool history --keychain-profile "$profile" >/dev/null 2>&1 ||
		die "the notarytool keychain profile \"$profile\" doesn't work; make it with xcrun notarytool store-credentials"
	printf '%s\n' "$profile"
}

windows_certificate() {
	local cert="${LEAFLET_WINDOWS_CERTIFICATE:-}"
	[[ -n "$cert" ]] || return 0
	cert="$(abs_path "$cert")"
	[[ -f "$cert" ]] || die "no Windows certificate at $cert (LEAFLET_WINDOWS_CERTIFICATE)"
	command -v osslsigncode >/dev/null 2>&1 || die "signing Windows builds on macOS needs osslsigncode (brew install osslsigncode)"
	[[ -n "${MYGO_WINDOWS_CERTIFICATE_PASSWORD:-}" ]] || warn "MYGO_WINDOWS_CERTIFICATE_PASSWORD is not set; the certificate must have no password"
	printf '%s\n' "$(cd "$(dirname "$cert")" && pwd)/$(basename "$cert")"
}

check_signed() {
	python3 -I - "$@" <<'EOF' || die "a Windows certificate was given, so every program of the build should be signed"
import struct, sys

def signed(path):
    try:
        with open(path, "rb") as f:
            data = f.read(4096)
        pe = struct.unpack_from("<I", data, 0x3c)[0]
        directories = {0x10b: 96, 0x20b: 112}[struct.unpack_from("<H", data, pe + 24)[0]]
        return struct.unpack_from("<I", data, pe + 24 + directories + 4 * 8 + 4)[0] != 0
    except (OSError, KeyError, struct.error):
        return False

sys.exit("\n".join(f"not signed: {path}" for path in sys.argv[1:] if not signed(path)) or None)
EOF
}

check_nsis() {
	command -v makensis >/dev/null 2>&1 || die "Windows installers need NSIS (brew install makensis)"
	[[ -n "${LEAFLET_WINDOWS_CERTIFICATE:-}" ]] || return 0
	# MyGo signs the installer's uninstaller through NSIS 3.08 or later,
	# and with an older one leaves it unsigned, which no check of the
	# build's programs sees: it is inside the installer.
	local version
	version="$(makensis -VERSION 2>/dev/null || true)"
	if ! [[ "$version" =~ ^v?([0-9]+)\.([0-9]+) ]] ||
		((10#${BASH_REMATCH[1]} < 3 || (10#${BASH_REMATCH[1]} == 3 && 10#${BASH_REMATCH[2]} < 8))); then
		die "a signed Windows build needs NSIS 3.08 or later, which signs the uninstaller, not ${version:-a makensis that gives no version} (brew upgrade makensis)"
	fi
}

utf8_locale() {
	[[ "$(locale charmap 2>/dev/null)" == "UTF-8" ]] && return
	export LC_ALL=en_US.UTF-8 LANG=en_US.UTF-8
}

update_key() {
	[[ -z "${MYGO_UPDATER_PRIVATE_KEY:-}" ]] || return 0
	local file
	file="$(abs_path "${LEAFLET_UPDATE_KEY:-$HOME/Library/Application Support/mygo/update-keys/Leaflet/mygo-update.key}")"
	[[ -f "$file" ]] || die "no update signing key at $file; set LEAFLET_UPDATE_KEY or MYGO_UPDATER_PRIVATE_KEY"
	printf '%s\n' "$file"
}

check_gh() {
	command -v gh >/dev/null 2>&1 || die "uploads need the GitHub CLI, gh"
	gh auth status >/dev/null 2>&1 || [[ -n "${GH_TOKEN:-}${GITHUB_TOKEN:-}" ]] || die "uploads need gh auth login, or GH_TOKEN"
}

# Asks gh for the release v$1, with the options after it, into RELEASE. No
# such release returns 1; any other failure dies, as none of the guards that
# ask may pass for want of an answer.
find_release() {
	local tag="v$1" repo err why status=0
	shift
	repo="$(config_value updates.github)"
	err="$(mktemp)"
	RELEASE="$(gh release view "$tag" --repo "$repo" "$@" 2>"$err")" || status=$?
	why="$(cat "$err")"
	rm -f "$err"
	((status != 0)) || return 0
	if grep -qi 'release not found' <<<"$why"; then return 1; fi
	die "could not ask GitHub for the release $tag of $repo: ${why:-gh release view failed}"
}

check_draft() {
	local repo
	repo="$(config_value updates.github)"
	if find_release "$1" --json isDraft --jq .isDraft && [[ "$RELEASE" != true ]]; then
		die "the release v$1 of $repo is already published; a new build needs a new version"
	fi
}

check_draft_commit() {
	local repo
	repo="$(config_value updates.github)"
	if find_release "$1" --json targetCommitish --jq .targetCommitish && [[ "$RELEASE" != "$2" ]]; then
		die "the draft release v$1 of $repo holds a build of $RELEASE, not of $2; delete the draft and run again"
	fi
}

# Dies unless the checkout is still the commit $1 with no file changed or
# added: one that git does not ignore may have gone into the build, as a
# .go file or a migration would, and the tag would not have it. A build has
# mygo.json changed for its run, so while it does, the copy it kept is the
# one compared.
check_unchanged() {
	local changed
	if $CONFIG_HELD; then
		changed="$(git status --porcelain --untracked-files=normal -- . ':(exclude)mygo.json')"
		[[ "$(git hash-object "$CONFIG_KEPT")" == "$(git rev-parse HEAD:mygo.json)" ]] || changed+=" mygo.json"
	else
		changed="$(git status --porcelain --untracked-files=normal)"
	fi
	[[ "$(git rev-parse HEAD)" == "$1" && -z "$changed" ]] ||
		die "the checkout changed while $1 was built, so the build is not of the commit its draft release would record; nothing was uploaded"
}

check_pushed() {
	local repo
	repo="$(config_value updates.github)"
	gh api "repos/$repo/commits/$1" --silent >/dev/null 2>&1 ||
		die "$repo does not have the commit $1, which its draft release would record; push it first"
}

update_manifests() {
	printf 'update-%s.json\n' darwin-arm64 darwin-amd64 darwin-universal windows-amd64 windows-arm64
}

missing_manifests() {
	local name
	while read -r name; do
		grep -qxF "$name" <<<"$1" || printf '%s\n' "$name"
	done < <(update_manifests)
}

# Prints, a line each, what is wrong with the files the update manifests
# on the release v$1 name, but for the manifests named after it: each
# archive and delta, at releases/download/v$1, must be a file of the
# release, of the size the manifest gives. Installed copies download them
# from there. Prints nothing with no such release.
manifest_problems() {
	local version="$1" repo tag dir status=0
	shift
	repo="$(config_value updates.github)"
	tag="v$version"
	find_release "$version" --json assets --jq '.assets[] | "\(.size) \(.name)"' || return 0
	dir="$(mktemp -d)"
	printf '%s\n' "$RELEASE" >"$dir/assets"
	mkdir "$dir/manifests"
	if grep -q ' update-.*\.json$' "$dir/assets" &&
		! gh release download "$tag" --repo "$repo" --pattern 'update-*.json' --dir "$dir/manifests" >/dev/null; then
		rm -rf "$dir"
		die "could not download the update manifests of the release $tag of $repo"
	fi
	python3 -I - "$dir" "https://github.com/$repo/releases/download/$tag/" "$@" <<'EOF' || status=$?
import json, os, sys
from urllib.parse import unquote
root, prefix, skip = sys.argv[1], sys.argv[2], set(sys.argv[3:])
sizes = {}
with open(os.path.join(root, "assets"), encoding="utf-8") as f:
    for line in f:
        size, _, name = line.rstrip("\n").partition(" ")
        if name:
            sizes[name] = int(size)
for name in sorted(os.listdir(os.path.join(root, "manifests"))):
    if name in skip:
        continue
    try:
        with open(os.path.join(root, "manifests", name), encoding="utf-8") as f:
            manifest = json.load(f)
        entries = [manifest, *(manifest.get("deltas") or [])]
        named = [(e.get("url") or "", e.get("size")) for e in entries]
    except (ValueError, AttributeError):
        print(f"{name} is no update manifest")
        continue
    for url, size in named:
        file = unquote(url[len(prefix):]) if url.startswith(prefix) else None
        if file is None:
            print(f"{name} names {url or 'no file'}, not a file of this release")
        elif file not in sizes:
            print(f"{name} names {file}, which the release does not have")
        elif sizes[file] != size:
            print(f"{name} names {file} of {size} bytes, and the release has one of {sizes[file]}")
EOF
	rm -rf "$dir"
	return "$status"
}

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

upload_release() {
	local version="$1" out="$2" notes="$3" commit="$4" repo tag dir f sum name found assets earlier
	shift 4
	local files=() manifests=()
	repo="$(config_value updates.github)"
	tag="v$version"
	[[ -f "$out/SHA256SUMS" ]] || die "no SHA256SUMS in $out, which lists what the build made to upload"
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
	if find_release "$version" --json assets --jq '.assets[].name'; then
		assets="$RELEASE"
		if grep -qx SHA256SUMS <<<"$assets"; then
			earlier="$(mktemp -d)"
			gh release download "$tag" --repo "$repo" --pattern SHA256SUMS --dir "$earlier"
			merge_checksums "$out/SHA256SUMS" "$earlier/SHA256SUMS"
			rm -rf "$earlier"
		fi
		# The manifests of these platforms come off first, and go back on
		# last: gh deletes each file before it uploads its replacement, so
		# a run that stops part way would leave them naming files gone, or
		# not those they were. Without them, the draft offers those
		# platforms no update.
		for f in ${manifests[@]+"${manifests[@]}"}; do
			name="$(basename "$f")"
			if grep -qxF "$name" <<<"$assets"; then
				info "taking $name off $tag of $repo until the files it names are up"
				gh release delete-asset "$tag" "$name" --repo "$repo" --yes >/dev/null
			fi
		done
	else
		info "creating the draft release $tag of $repo"
		gh release create "$tag" --repo "$repo" --draft --target "$commit" --title "$tag" --notes-file "$notes" >/dev/null
	fi
	files+=("$out/SHA256SUMS")
	info "uploading ${#files[@]} files, then ${#manifests[@]} manifests, to $tag of $repo"
	if ((${#files[@]} > 0)); then gh release upload "$tag" --repo "$repo" --clobber "${files[@]}"; fi
	if ((${#manifests[@]} > 0)); then gh release upload "$tag" --repo "$repo" --clobber "${manifests[@]}"; fi
}
