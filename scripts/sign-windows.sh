#!/usr/bin/env bash
# Signs the Windows program $1 with osslsigncode, as go tool mygo build runs
# it for scripts/build-release.sh (windows.signCommand): with the .pfx
# LEAFLET_WINDOWS_CERTIFICATE names, in full. The password,
# MYGO_WINDOWS_CERTIFICATE_PASSWORD, goes through a pipe: on the command
# line, where MyGo puts it when it signs itself, ps shows it to every user
# of the Mac.
set -euo pipefail

file="$1"
signed="$file.signed"
trap 'rm -f "$signed"' EXIT
args=(sign -pkcs12 "$LEAFLET_WINDOWS_CERTIFICATE" -h sha256 -ts http://timestamp.digicert.com -in "$file" -out "$signed")
if [[ -n "${MYGO_WINDOWS_CERTIFICATE_PASSWORD:-}" ]]; then
	osslsigncode "${args[@]}" -readpass <(printf '%s' "$MYGO_WINDOWS_CERTIFICATE_PASSWORD")
else
	osslsigncode "${args[@]}"
fi
mv -f "$signed" "$file"
