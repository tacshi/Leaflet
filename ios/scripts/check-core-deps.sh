#!/bin/bash

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
