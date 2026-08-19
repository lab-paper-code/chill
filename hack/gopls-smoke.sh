#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

expected_gopls="${CHILL_GOPLS_VERSION:-v0.23.0}"
expected_go="${CHILL_GOPLS_GO_VERSION:-go1.26.4}"

gopls_command() {
  CHILL_GOPLS_GO_VERSION="$expected_go" ./hack/gopls-chill.sh "$@"
}

position_of() {
  local file="$1"
  local pattern="$2"
  local symbol="$3"
  local line
  local source
  local prefix

  line="$(rg -n -m1 "$pattern" "$file" | cut -d: -f1)"
  source="$(sed -n "${line}p" "$file")"
  prefix="${source%"$symbol"*}"
  printf '%s:%s:%s\n' "$file" "$line" "$((${#prefix} + 1))"
}

require_contains() {
  local label="$1"
  local output="$2"
  local expected="$3"

  if [[ "$output" != *"$expected"* ]]; then
    printf '%s: expected %q in output\n%s\n' "$label" "$expected" "$output" >&2
    exit 1
  fi
}

go_version="$(GOTOOLCHAIN="$expected_go" go version)"
require_contains go-toolchain "$go_version" "$expected_go"

gopls_version="$(gopls_command version)"
require_contains gopls-version "$gopls_version" "$expected_gopls"

workspace_stats="$(gopls_command stats -anon)"
require_contains workspace-go-version "$workspace_stats" "\"GoVersion\": \"$expected_go\""
require_contains workspace-diagnostics "$workspace_stats" '"Diagnostics": 0'

mapfile -d '' go_files < <(
  find . -type f -name '*.go' -print0
)
diagnostics="$(gopls_command check -severity=error "${go_files[@]}")"
if [[ -n "$diagnostics" ]]; then
  printf 'gopls diagnostics were not empty:\n%s\n' "$diagnostics" >&2
  exit 1
fi

freezer_interface="$(position_of \
  internal/profiling/profiler/controller/intent_reconciler.go \
  'type RunIntentFreezer interface' \
  'RunIntentFreezer')"
freezer_implementations="$(gopls_command implementation "$freezer_interface")"
require_contains freezer-implementation "$freezer_implementations" \
  'internal/profiling/profiler/controller/intent_freezer.go'

setup_call="$(position_of \
  cmd/profiler/controller_setup.go \
  'reconciler\.SetupWithManager' \
  'SetupWithManager')"
setup_definition="$(gopls_command definition "$setup_call")"
require_contains setup-definition "$setup_definition" \
  'internal/profiling/profiler/controller/intent_setup.go'

intent_frozen="$(position_of \
  api/v1alpha1/profilingrun_types.go \
  'IntentFrozen bool' \
  'IntentFrozen')"
intent_frozen_references="$(gopls_command references "$intent_frozen")"
require_contains intent-frozen-freezer-reference "$intent_frozen_references" \
  'internal/profiling/profiler/controller/intent_freezer.go'
require_contains intent-frozen-projection-reference "$intent_frozen_references" \
  'internal/profiling/profiler/controller/projection.go'
require_contains intent-frozen-integration-reference "$intent_frozen_references" \
  'test/integration/operator/profilingrun_api_test.go'

reconcile="$(position_of \
  internal/profiling/profiler/controller/intent_reconciler.go \
  'func \(r \*RunReconciler\) Reconcile' \
  'Reconcile')"
reconcile_hierarchy="$(gopls_command call_hierarchy "$reconcile")"
require_contains reconcile-freeze-callee "$reconcile_hierarchy" \
  'internal/profiling/profiler/controller/intent_reconciler.go:'

printf 'phase1.go_toolchain=%s\n' "$expected_go"
printf 'phase1.gopls=%s\n' "$expected_gopls"
printf 'phase1.workspace_diagnostics=0\n'
printf 'phase1.file_errors=0\n'
printf 'phase1.freezer_implementation=pass\n'
printf 'phase1.setup_definition=pass\n'
printf 'phase1.intent_frozen_references=pass\n'
printf 'phase1.reconcile_event_edge=framework-derived\n'
