#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
codegraph="$repo_root/hack/codegraph-chill.sh"
fixture_dir="$repo_root/hack/testdata/codegraph-freshness"
created="$repo_root/hack/codegraph_watcher_probe.go"
renamed="$repo_root/hack/codegraph_watcher_renamed.go"
temp_root="$(mktemp -d)"
responses="$temp_root/responses.jsonl"
watcher_complete=false

cleanup() {
  rm -f "$created" "$renamed"
  if [[ "$watcher_complete" != true ]]; then
    if [[ -s "$responses" ]]; then
      echo "codegraph watcher: MCP responses received before failure:" >&2
      jq -Rr 'fromjson? | select(has("id")) | "  id=\(.id) error=\(.error.message // "none")"' \
        "$responses" >&2 || true
    fi
    "$codegraph" sync --quiet "$repo_root" >/dev/null 2>&1 || true
  fi
  rm -rf "$temp_root"
}
trap cleanup EXIT

tool_call() {
  local id="$1"
  local query="$2"

  jq -cn \
    --argjson id "$id" \
    --arg query "$query" \
    --arg projectPath "$repo_root" \
    '{
      jsonrpc: "2.0",
      id: $id,
      method: "tools/call",
      params: {
        name: "codegraph_explore",
        arguments: {
          query: $query,
          maxFiles: 2,
          projectPath: $projectPath
        }
      }
    }'
}

response_text() {
  local id="$1"

  jq -Rsr --argjson id "$id" '
    [
      split("\n")[]
      | try fromjson catch empty
      | select(.id == $id)
      | .result.content[]?
      | select(.type == "text")
      | .text
    ]
    | join("\n")
  ' \
    "$responses"
}

rm -f "$created" "$renamed"
"$codegraph" sync --quiet "$repo_root"

{
  jq -cn '{
    jsonrpc: "2.0",
    id: 1,
    method: "initialize",
    params: {
      protocolVersion: "2024-11-05",
      capabilities: {},
      clientInfo: {name: "chill-codegraph-check", version: "1.0"}
    }
  }'
  jq -cn '{jsonrpc: "2.0", method: "notifications/initialized", params: {}}'
  sleep 2

  cp "$fixture_dir/created.go.txt" "$created"
  sleep 3
  tool_call 2 CodeGraphFreshnessCreatedMarker
  sleep 2

  cp "$fixture_dir/modified.go.txt" "$created"
  sleep 3
  tool_call 3 CodeGraphFreshnessModifiedMarker
  tool_call 4 CodeGraphFreshnessCreatedMarker
  sleep 2

  mv "$created" "$renamed"
  sleep 3
  tool_call 5 'CodeGraphFreshnessModifiedMarker codegraph_watcher_renamed.go'
  sleep 2

  rm -f "$renamed"
  sleep 3
  tool_call 6 CodeGraphFreshnessModifiedMarker
  sleep 2
} | "$codegraph" serve --mcp --path "$repo_root" >"$responses"

jq -Rs -e '
  [split("\n")[] | try fromjson catch empty]
  | any(.id == 1 and .result.serverInfo == {name: "codegraph", version: "1.4.1"})
' \
  "$responses" >/dev/null
echo "codegraph.watcher.mcp_handshake=pass:1.4.1"

created_text="$(response_text 2)"
[[ "$created_text" == *'hack/codegraph_watcher_probe.go'* ]]
[[ "$created_text" == *'CodeGraphFreshnessCreatedMarker(function)'* ]]
echo "codegraph.watcher.create_untracked=pass"

modified_text="$(response_text 3)"
[[ "$modified_text" == *'hack/codegraph_watcher_probe.go'* ]]
[[ "$modified_text" == *'CodeGraphFreshnessModifiedMarker(function)'* ]]
[[ "$(response_text 4)" != *'CodeGraphFreshnessCreatedMarker(function)'* ]]
echo "codegraph.watcher.modify=pass"

renamed_text="$(response_text 5)"
[[ "$renamed_text" == *'hack/codegraph_watcher_renamed.go'* ]]
[[ "$renamed_text" == *'CodeGraphFreshnessModifiedMarker(function)'* ]]
echo "codegraph.watcher.rename=pass"

deleted_text="$(response_text 6)"
[[ "$deleted_text" != *'CodeGraphFreshnessModifiedMarker(function)'* ]]
[[ "$deleted_text" != *'hack/codegraph_watcher_renamed.go'* ]]
echo "codegraph.watcher.delete=pass"

status="$($codegraph status --json "$repo_root")"
jq -e '
  .index.state == "complete" and
  .index.pendingRefs == 0 and
  .pendingChanges.added == 0 and
  .pendingChanges.modified == 0 and
  .pendingChanges.removed == 0
' <<<"$status" >/dev/null
echo "codegraph.watcher.final_status=clean"
echo "codegraph.watcher.mode=mcp-auto-sync"

watcher_complete=true
