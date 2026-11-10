#!/usr/bin/env bash
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
