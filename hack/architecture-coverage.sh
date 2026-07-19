#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

module="$(go list -m -f '{{.Path}}')"
config="$repo_root/.go-arch-lint.yml"

components=(
  api
  entrypoints
  platform-core
  device-class
  operator-console
  operator
  evidence-core
  evidence-s3
  power-core
  power-edgemetrics
  model-compat
  profile-derivation
  profiling-run
  profiler-app
  profiler-controller
  profiler-admission
  profiler-evidence
  profiler-evidence-kube
  profiler-worker
)

component_for() {
  local package_path="$1"

  case "$package_path" in
    api/v1alpha1) echo api ;;
    cmd|cmd/*) echo entrypoints ;;
    internal/component|internal/defaults|internal/kubeconfig|internal/metadata|internal/nodeprobe)
      echo platform-core
      ;;
    internal/deviceclass) echo device-class ;;
    internal/operatorconsole) echo operator-console ;;
    internal/operator/*) echo operator ;;
    internal/evidencestore) echo evidence-core ;;
    internal/evidencestore/s3) echo evidence-s3 ;;
    internal/powerobserver) echo power-core ;;
    internal/powerobserver/edgemetrics) echo power-edgemetrics ;;
    internal/modelcompat) echo model-compat ;;
    internal/profilederivation) echo profile-derivation ;;
    internal/profilingrun) echo profiling-run ;;
    internal/profiler) echo profiler-app ;;
    internal/profiler/controller) echo profiler-controller ;;
    internal/profiler/admission) echo profiler-admission ;;
    internal/profiler/evidence) echo profiler-evidence ;;
    internal/profiler/evidence/kube) echo profiler-evidence-kube ;;
    internal/profiler/worker) echo profiler-worker ;;
    *) return 1 ;;
  esac
}

for component in "${components[@]}"; do
  if ! rg -q "^  ${component}:" "$config"; then
    echo "architecture error: component '$component' is missing from .go-arch-lint.yml" >&2
    exit 1
  fi
done

mapfile -t packages < <(
  go list -f '{{.ImportPath}}' ./api/... ./cmd/... ./internal/... | LC_ALL=C sort
)
mapfile -t excluded_test_packages < <(
  go list -f '{{.ImportPath}}' ./test/... | LC_ALL=C sort
)

declare -A component_counts=()
declare -A production_edges=()

for import_path in "${packages[@]}"; do
  relative_path="${import_path#"$module"/}"
  if ! component="$(component_for "$relative_path")"; then
    echo "architecture error: unmapped production package '$relative_path'" >&2
    exit 1
  fi
  component_counts["$component"]=$(( ${component_counts["$component"]:-0} + 1 ))
done

while IFS='|' read -r source imports; do
  for dependency in $imports; do
    if [[ "$dependency" == "$module/"* ]]; then
      production_edges["$source->$dependency"]=1
    fi
  done
done < <(
  go list -f '{{.ImportPath}}|{{join .Imports " "}}' ./api/... ./cmd/... ./internal/...
)

declare -A test_edges=()
while IFS='|' read -r source test_imports external_test_imports; do
  for dependency in $test_imports $external_test_imports; do
    if [[ "$dependency" == "$module/"* ]]; then
      test_edges["$source->$dependency"]=1
    fi
  done
done < <(
  go list -f '{{.ImportPath}}|{{join .TestImports " "}}|{{join .XTestImports " "}}' \
    ./api/... ./cmd/... ./internal/...
)

test_only_edges=0
for edge in "${!test_edges[@]}"; do
  if [[ -z "${production_edges["$edge"]+present}" ]]; then
    test_only_edges=$((test_only_edges + 1))
  fi
done

declare -A excluded_test_edges=()
while IFS='|' read -r source imports test_imports external_test_imports; do
  for dependency in $imports $test_imports $external_test_imports; do
    if [[ "$dependency" == "$module/"* ]]; then
      excluded_test_edges["$source->$dependency"]=1
    fi
  done
done < <(
  go list -f '{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}}|{{join .XTestImports " "}}' \
    ./test/...
)

printf 'architecture.production_packages=%d\n' "${#packages[@]}"
printf 'architecture.mapped_packages=%d\n' "${#packages[@]}"
printf 'architecture.components=%d\n' "${#components[@]}"
for component in "${components[@]}"; do
  printf 'architecture.component.%s=%d\n' \
    "$component" "${component_counts["$component"]:-0}"
done
printf 'architecture.test_files_enforced=false\n'
printf 'architecture.production_package_test_only_internal_edges=%d\n' "$test_only_edges"
printf 'architecture.excluded_test_packages=%d\n' "${#excluded_test_packages[@]}"
printf 'architecture.excluded_test_internal_edges=%d\n' "${#excluded_test_edges[@]}"
