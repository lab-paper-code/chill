#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
codegraph="$repo_root/hack/codegraph-chill.sh"
session="${CODEGRAPH_PILOT_SESSION:-1}"
module="$(cd "$repo_root" && go list -m -f '{{.Path}}')"

if [[ ! "$session" =~ ^[123]$ ]]; then
  echo "codegraph pilot: CODEGRAPH_PILOT_SESSION must be 1, 2, or 3" >&2
  exit 1
fi

temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT

status_json="$($codegraph status --json "$repo_root")"
if ! jq -e '
  .initialized == true and
  .version == "1.4.1" and
  .index.state == "complete" and
  .index.pendingRefs == 0 and
  .pendingChanges.added == 0 and
  .pendingChanges.modified == 0 and
  .pendingChanges.removed == 0
' <<<"$status_json" >/dev/null; then
  echo "codegraph pilot: index is not clean and complete; run 'make codegraph-sync'" >&2
  jq '.' <<<"$status_json" >&2
  exit 1
fi

query_json() {
  "$codegraph" query --path "$repo_root" --limit 10000 --json "$1"
}

contains_path() {
  local file="$1"
  jq -e --arg file "$file" 'any(.[]; .node.filePath == $file)' >/dev/null
}

q1_symbols="$(query_json IntentReconciler)"
q1_callers="$($codegraph callers --path "$repo_root" --limit 50 --json NewIntentReconciler)"
q1=pass
for file in \
  internal/profiler/controller/intent_setup.go \
  internal/profiler/controller/intent_reconciler.go; do
  if ! contains_path "$file" <<<"$q1_symbols"; then q1=fail; fi
done
if ! jq -e 'any(.callers[]; .filePath == "cmd/profiler/controller_setup.go")' \
  <<<"$q1_callers" >/dev/null; then
  q1=fail
fi

q2_interface="$(query_json RunIntentFreezer)"
q2_production="$(query_json IntentFreezer)"
q2_fake="$(query_json recordingIntentFreezer)"
q2=pass
if ! contains_path internal/profiler/controller/intent_reconciler.go <<<"$q2_interface"; then q2=fail; fi
if ! contains_path internal/profiler/controller/intent_freezer.go <<<"$q2_production"; then q2=fail; fi
if ! contains_path internal/profiler/controller/intent_reconciler_test.go <<<"$q2_fake"; then q2=fail; fi

codegraph_imports="$temp_root/codegraph-imports"
go_imports="$temp_root/go-imports"
query_json "$module" | jq -r --arg modulePrefix "$module/" '
  .[]
  | select(.node.kind == "import")
  | select(.node.name | startswith($modulePrefix))
  | select(.node.filePath | test("^(api|cmd|internal)/"))
  | select(.node.filePath | endswith("_test.go") | not)
  | ((.node.filePath | split("/") | .[0:-1] | join("/"))) as $source
  | "\($modulePrefix)\($source) -> \(.node.name)"
' | LC_ALL=C sort -u >"$codegraph_imports"

go list -f '{{.ImportPath}} {{join .Imports " "}}' \
  "$repo_root/api/..." "$repo_root/cmd/..." "$repo_root/internal/..." |
  awk -v module="$module/" '
    {
      source = $1
      for (i = 2; i <= NF; i++) {
        if (index($i, module) == 1) print source " -> " $i
      }
    }
  ' | LC_ALL=C sort -u >"$go_imports"

if ! diff -u "$go_imports" "$codegraph_imports" >/dev/null; then
  echo "codegraph pilot: CodeGraph package edges disagree with go list" >&2
  diff -u "$go_imports" "$codegraph_imports" >&2 || true
  exit 1
fi

q5_candidates="$(query_json IntentDigest)"
if contains_path api/v1alpha1/profilingrun_types.go <<<"$q5_candidates"; then
  q5=pass
else
  q5=known-miss
fi
for file in \
  api/v1alpha1/profilingrun_types.go \
  internal/profiler/controller/intent_freezer.go \
  internal/profiler/controller/projection.go \
  test/integration/operator/profilingrun_api_test.go; do
  if ! rg -q 'IntentDigest' "$repo_root/$file"; then
    echo "codegraph pilot: authoritative IntentDigest source missing in $file" >&2
    exit 1
  fi
done

q6_candidates="$(query_json 'profiler enabled')"
if contains_path charts/chill/templates/profiler_config.yaml <<<"$q6_candidates"; then
  q6=pass
else
  q6=known-miss
fi
baseline="$($repo_root/hack/code-intelligence-baseline.sh)"
for expected in \
  baseline.helm_default_resources=23 \
  baseline.helm_profiler_resources=30; do
  if ! grep -Fxq "$expected" <<<"$baseline"; then
    echo "codegraph pilot: Helm authority changed; refresh the golden answer" >&2
    exit 1
  fi
done

printf 'codegraph.pilot.session=%s\n' "$session"
printf 'codegraph.pilot.version=%s\n' "$(jq -r '.version' <<<"$status_json")"
printf 'codegraph.pilot.files=%s\n' "$(jq -r '.fileCount' <<<"$status_json")"
printf 'codegraph.pilot.nodes=%s\n' "$(jq -r '.nodeCount' <<<"$status_json")"
printf 'codegraph.pilot.edges=%s\n' "$(jq -r '.edgeCount' <<<"$status_json")"
printf 'codegraph.pilot.q1_candidates=%s\n' "$q1"
printf 'codegraph.pilot.q1_event_edge=framework-derived-source-required\n'
printf 'codegraph.pilot.q2_candidates=%s\n' "$q2"
printf 'codegraph.pilot.q2_implementation_authority=gopls-required\n'
printf 'codegraph.pilot.q3_q4_package_edges=pass:%s\n' "$(wc -l <"$go_imports")"
printf 'codegraph.pilot.q5_intent_digest=%s\n' "$q5"
printf 'codegraph.pilot.q5_source_verification=pass\n'
printf 'codegraph.pilot.q6_helm_resources=%s\n' "$q6"
printf 'codegraph.pilot.q6_helm_verification=pass:23->30\n'
printf 'codegraph.pilot.affected_tests=unapproved\n'
printf 'codegraph.pilot.acceptance=in-progress:%s/3-sessions\n' "$session"
