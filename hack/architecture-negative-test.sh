#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tool="${CHILL_GO_ARCH_LINT:-$repo_root/bin/go-arch-lint}"
fixture="$repo_root/hack/testdata/architecture-violation/powerobserver_forbidden.go.txt"

if [[ ! -x "$tool" ]]; then
  echo "architecture negative test: missing executable $tool; run 'make go-arch-lint'" >&2
  exit 1
fi

temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT
project="$temp_root/chill"
mkdir -p "$project"

cp "$repo_root/.go-arch-lint.yml" "$repo_root/go.mod" "$repo_root/go.sum" "$project/"
cp -R "$repo_root/api" "$repo_root/cmd" "$repo_root/internal" "$project/"
cp "$fixture" "$project/internal/powerobserver/architecture_violation.go"

set +e
output="$($tool check \
  --project-path "$project" \
  --arch-file "$project/.go-arch-lint.yml" \
  --output-color=false 2>&1)"
status=$?
set -e

if [[ $status -eq 0 ]]; then
  echo "architecture negative test: intentional power-core -> api import was accepted" >&2
  exit 1
fi

if ! grep -Fq \
  "Component power-core shouldn't depend on github.com/lab-paper-code/chill/api/v1alpha1" \
  <<<"$output"; then
  echo "architecture negative test: linter failed for an unexpected reason" >&2
  echo "$output" >&2
  exit 1
fi

echo "architecture.negative_fixture=pass"
echo "architecture.expected_rejection=power-core->api"
