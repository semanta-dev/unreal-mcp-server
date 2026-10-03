#!/usr/bin/env bash
# Print the CHANGELOG.md section for a tag (e.g. v2.0.1) — the release body.
# Fails when the tag has no section, so a release cannot ship without notes.
set -euo pipefail
cd "$(dirname "$0")/.."
tag="${1:?usage: release-notes.sh <tag>}"
notes="$(awk -v t="$tag" '
  $0 ~ "^## " t "( |$)" { p = 1; next }
  /^## / { if (p) exit }
  p' CHANGELOG.md)"
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "CHANGELOG.md has no section for $tag" >&2
  exit 1
fi
printf '%s\n' "$notes"
