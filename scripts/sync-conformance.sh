#!/usr/bin/env bash
# Copy the language-neutral conformance cases of cleanedweb-openapi into testdata/conformance.
#
# The cases are vendored so a test run needs no network; this script refreshes them. By default it
# reads a sibling checkout (../cleanedweb-openapi). Set CLEANEDWEB_OPENAPI_DIR to use another
# checkout, or pass --remote to clone it with the gh CLI. Pass --ref <branch|tag|sha> with --remote.
#
#   scripts/sync-conformance.sh                  # from ../cleanedweb-openapi
#   scripts/sync-conformance.sh --remote         # gh repo clone cleanedweb/cleanedweb-openapi (main)
#   scripts/sync-conformance.sh --remote --ref v0.1.0
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
src="${CLEANEDWEB_OPENAPI_DIR:-$here/../cleanedweb-openapi}"
remote=0
ref=""
while [ $# -gt 0 ]; do
  case "$1" in
    --remote) remote=1 ;;
    --ref) ref="${2:?--ref needs a value}"; shift ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

tmp=""
if [ "$remote" = 1 ]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  gh repo clone cleanedweb/cleanedweb-openapi "$tmp/repo" -- --quiet ${ref:+--branch "$ref"}
  src="$tmp/repo"
fi

[ -d "$src/tests/cases" ] || { echo "no tests/cases in $src" >&2; exit 1; }
dest="$here/testdata/conformance"
rm -rf "$dest"
mkdir -p "$dest"
cp -R "$src/tests/cases" "$dest/cases"
cp "$src/tests/case.schema.json" "$dest/case.schema.json"
cp "$src/tests/README.md" "$dest/README.md"
sha="$(git -C "$src" rev-parse HEAD 2>/dev/null || echo unknown)"
printf 'cleanedweb-openapi %s\n' "$sha" > "$dest/SOURCE"
echo "synced $(ls "$dest/cases" | wc -l | tr -d ' ') case files from $src ($sha)"
