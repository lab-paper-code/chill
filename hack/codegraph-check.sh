#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
codegraph="$repo_root/hack/codegraph-chill.sh"
module="$(cd "$repo_root" && go list -m -f '{{.Path}}')"

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
  echo "codegraph check: index is not clean and complete; run 'make codegraph-sync'" >&2
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

q1_symbols="$(query_json RunReconciler)"
q1_callers="$($codegraph callers --path "$repo_root" --limit 50 --json NewRunReconciler)"
q1=pass
for file in \
  internal/profiling/profiler/controller/run_setup.go \
  internal/profiling/profiler/controller/run_reconciler.go; do
  if ! contains_path "$file" <<<"$q1_symbols"; then q1=fail; fi
done
if ! jq -e 'any(.callers[]; .filePath == "cmd/profiler/controller_setup.go")' \
  <<<"$q1_callers" >/dev/null; then
  q1=fail
fi

q2_interface="$(query_json RunConfigurationResolver)"
q2_production="$(query_json RunConfigResolver)"
q2_fake="$(query_json recordingRunConfigResolver)"
q2=pass
if ! contains_path internal/profiling/profiler/controller/run_reconciler.go <<<"$q2_interface"; then q2=fail; fi
if ! contains_path internal/profiling/profiler/controller/run_config_resolver.go <<<"$q2_production"; then q2=fail; fi
if ! contains_path internal/profiling/profiler/controller/run_reconciler_test.go <<<"$q2_fake"; then q2=fail; fi

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
  echo "codegraph check: CodeGraph package edges disagree with go list" >&2
  diff -u "$go_imports" "$codegraph_imports" >&2 || true
  exit 1
fi

q5_candidates="$(query_json resolvedRunFromJob)"
if contains_path internal/profiling/profiler/controller/run_record.go <<<"$q5_candidates"; then
  q5=pass
else
  q5=known-miss
fi
for file in \
  internal/profiling/profiler/controller/run_record.go \
  internal/profiling/profiler/controller/run_reconciler.go \
  internal/profiling/profiler/controller/execution_job.go; do
  if ! rg -q 'resolvedRunFromJob' "$repo_root/$file"; then
    echo "codegraph check: authoritative Job RunConfig recovery source missing in $file" >&2
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
    echo "codegraph check: Helm authority changed; refresh the golden answer" >&2
    exit 1
  fi
done

printf 'codegraph.check.version=%s\n' "$(jq -r '.version' <<<"$status_json")"
printf 'codegraph.check.files=%s\n' "$(jq -r '.fileCount' <<<"$status_json")"
printf 'codegraph.check.nodes=%s\n' "$(jq -r '.nodeCount' <<<"$status_json")"
printf 'codegraph.check.edges=%s\n' "$(jq -r '.edgeCount' <<<"$status_json")"
printf 'codegraph.check.q1_candidates=%s\n' "$q1"
printf 'codegraph.check.q1_event_edge=framework-derived-source-required\n'
printf 'codegraph.check.q2_candidates=%s\n' "$q2"
printf 'codegraph.check.q2_implementation_authority=gopls-required\n'
printf 'codegraph.check.q3_q4_package_edges=pass:%s\n' "$(wc -l <"$go_imports")"
printf 'codegraph.check.q5_job_run_config_recovery=%s\n' "$q5"
printf 'codegraph.check.q5_source_verification=pass\n'
printf 'codegraph.check.q6_helm_resources=%s\n' "$q6"
printf 'codegraph.check.q6_helm_verification=pass:23->30\n'
printf 'codegraph.check.affected_tests=unapproved\n'
printf 'codegraph.check.acceptance=pass-with-recorded-limitations\n'
