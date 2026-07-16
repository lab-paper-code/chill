#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
codegraph="$repo_root/hack/codegraph-chill.sh"
fixture_dir="$repo_root/hack/testdata/codegraph-freshness"
created="$repo_root/hack/codegraph_freshness_probe.go"
renamed="$repo_root/hack/codegraph_freshness_renamed.go"

cleanup() {
  rm -f "$created" "$renamed"
  "$codegraph" sync --quiet "$repo_root" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

query_count() {
  "$codegraph" query --path "$repo_root" --limit 20 --json "$1" | jq 'length'
}

cp "$fixture_dir/created.go.txt" "$created"
"$codegraph" sync --quiet "$repo_root"
[[ "$(query_count CodeGraphFreshnessCreatedMarker)" -gt 0 ]]
echo "codegraph.freshness.create_untracked=pass"

cp "$fixture_dir/modified.go.txt" "$created"
"$codegraph" sync --quiet "$repo_root"
[[ "$(query_count CodeGraphFreshnessCreatedMarker)" -eq 0 ]]
[[ "$(query_count CodeGraphFreshnessModifiedMarker)" -gt 0 ]]
echo "codegraph.freshness.modify=pass"

mv "$created" "$renamed"
"$codegraph" sync --quiet "$repo_root"
renamed_result="$($codegraph query --path "$repo_root" --limit 20 --json CodeGraphFreshnessModifiedMarker)"
jq -e 'any(.[]; .node.filePath == "hack/codegraph_freshness_renamed.go")' \
  <<<"$renamed_result" >/dev/null
echo "codegraph.freshness.rename=pass"

rm -f "$renamed"
"$codegraph" sync --quiet "$repo_root"
[[ "$(query_count CodeGraphFreshnessModifiedMarker)" -eq 0 ]]
echo "codegraph.freshness.delete=pass"

status="$($codegraph status --json "$repo_root")"
jq -e '
  .index.state == "complete" and
  .pendingChanges.added == 0 and
  .pendingChanges.modified == 0 and
  .pendingChanges.removed == 0
' <<<"$status" >/dev/null
echo "codegraph.freshness.final_status=clean"
echo "codegraph.freshness.mode=explicit-sync"
