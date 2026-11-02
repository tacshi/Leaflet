#!/bin/bash
# Fails if the Go core for iOS (./mobile) depends on MyGo's windowing
# packages, which load AppKit and lock the main thread, or on purego.
# MyGo's i18n and its locale catalogs are allowed, since locales/ is
# built on them, and so is its Markdown parser, which internal/doc uses
# and which imports only the standard library.

set -euo pipefail

cd "$(dirname "$0")/../.."

allowed='^github\.com/egoist/mygo/(i18n|internal/i18nlink|internal/l10n|locales/[A-Za-z-]+|ui/markdown/parser)$'

deps=$(GOOS=ios GOARCH=arm64 CGO_ENABLED=1 go list -deps ./mobile)
bad=$(grep -E '^github\.com/(egoist/mygo|ebitengine/purego)($|/)' <<<"$deps" | grep -Ev "$allowed" || true)

if [ -n "$bad" ]; then
	echo "The iOS core must not depend on these packages:" >&2
	echo "$bad" >&2
	echo "Run 'go mod why -m <module>' or 'go list -deps ./mobile' with GOOS=ios to see the path." >&2
	exit 1
fi
echo "The iOS core is free of MyGo's windowing packages and purego."
