# Shared by the build and publish scripts; source it.

die() {
	printf '%s: %s\n' "$(basename "$0")" "$*" >&2
	exit 1
}
info() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# check_version fails unless $1 looks like 1.2.3.
check_version() {
	[[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "the version must look like 1.2.3, not $1"
}

# config_value prints the value in mygo.json at the dotted keys $1, as in
# "version" or "updates.github".
config_value() {
	python3 -I -c '
import json, sys
value = json.load(open(sys.argv[1]))
for key in sys.argv[2].split("."):
    value = value[key]
print(value)' "$ROOT_DIR/mygo.json" "$1"
}

# patch_config changes mygo.json for this run only: each argument is
# <dotted key>=<value>, which sets the value, or -<dotted key>, which
# removes the key. The first call keeps the committed file, which is put
# back when the script exits.
patch_config() {
	if [[ -z "${CONFIG_BACKUP:-}" ]]; then
		CONFIG_BACKUP="$(mktemp)"
		cp "$ROOT_DIR/mygo.json" "$CONFIG_BACKUP"
		trap 'cp "$CONFIG_BACKUP" "$ROOT_DIR/mygo.json"; rm -f "$CONFIG_BACKUP"' EXIT
	fi
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
	[[ -f "$cert" ]] || die "no Windows certificate at $cert (LEAFLET_WINDOWS_CERTIFICATE)"
	command -v osslsigncode >/dev/null 2>&1 || die "signing Windows builds on macOS needs osslsigncode (brew install osslsigncode)"
	[[ -n "${MYGO_WINDOWS_CERTIFICATE_PASSWORD:-}" ]] || warn "MYGO_WINDOWS_CERTIFICATE_PASSWORD is not set; the certificate must have no password"
	printf '%s\n' "$(cd "$(dirname "$cert")" && pwd)/$(basename "$cert")"
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

# load_update_key exports MYGO_UPDATER_PRIVATE_KEY, which signs updates, from
# the file LEAFLET_UPDATE_KEY names (by default the one `go tool mygo keygen`
# wrote for Leaflet) unless it is set.
load_update_key() {
	[[ -n "${MYGO_UPDATER_PRIVATE_KEY:-}" ]] && { export MYGO_UPDATER_PRIVATE_KEY; return; }
	local file="${LEAFLET_UPDATE_KEY:-$HOME/Library/Application Support/mygo/update-keys/Leaflet/mygo-update.key}"
	[[ -f "$file" ]] || die "no update signing key at $file; set LEAFLET_UPDATE_KEY or MYGO_UPDATER_PRIVATE_KEY"
	MYGO_UPDATER_PRIVATE_KEY="$(<"$file")"
	export MYGO_UPDATER_PRIVATE_KEY
}

# check_gh fails unless the GitHub CLI is there to upload with, signed in.
check_gh() {
	command -v gh >/dev/null 2>&1 || die "uploads need the GitHub CLI, gh"
	gh auth status >/dev/null 2>&1 || [[ -n "${GH_TOKEN:-}${GITHUB_TOKEN:-}" ]] || die "uploads need gh auth login, or GH_TOKEN"
}
