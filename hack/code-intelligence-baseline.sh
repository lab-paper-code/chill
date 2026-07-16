#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

module="$(go list -m)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

non_spike_packages="$tmp_dir/non-spike-packages"
internal_edges="$tmp_dir/internal-edges"
default_manifest="$tmp_dir/default.yaml"
profiler_manifest="$tmp_dir/profiler.yaml"
default_resources="$tmp_dir/default-resources"
profiler_resources="$tmp_dir/profiler-resources"

go list ./... | awk '!/\/spikes(\/|$)/' | sort >"$non_spike_packages"
go list -f '{{.ImportPath}} {{join .Imports " "}}' ./... |
  awk -v module="$module" '
    $1 !~ /\/spikes(\/|$)/ {
      source = $1
      for (i = 2; i <= NF; i++) {
        if (index($i, module) == 1 && $i !~ /\/spikes(\/|$)/) {
          print source " -> " $i
        }
      }
    }
  ' | sort -u >"$internal_edges"

helm template chill charts/chill --namespace chill-system >"$default_manifest"
helm template chill charts/chill --namespace chill-system \
  --set profiler.enabled=true >"$profiler_manifest"

extract_resources() {
  local manifest="$1"
  local output="$2"

  awk '
    /^---[[:space:]]*$/ {
      if (kind != "" && name != "") print kind "/" name
      kind = ""
      name = ""
      in_metadata = 0
      next
    }
    /^kind:[[:space:]]*/ {
      kind = $2
      next
    }
    /^metadata:[[:space:]]*$/ {
      in_metadata = 1
      next
    }
    in_metadata && /^  name:[[:space:]]*/ {
      name = $2
      in_metadata = 0
      next
    }
    END {
      if (kind != "" && name != "") print kind "/" name
    }
  ' "$manifest" | sort -u >"$output"
}

extract_resources "$default_manifest" "$default_resources"
extract_resources "$profiler_manifest" "$profiler_resources"

printf 'baseline.commit=%s\n' "$(git rev-parse HEAD)"
printf 'baseline.go_version=%s\n' "$(go version | awk '{print $3}')"
printf 'baseline.module_go=%s\n' "$(awk '$1 == "go" { print $2; exit }' go.mod)"
printf 'baseline.go_files_non_spike=%s\n' \
  "$(find . -path './spikes' -prune -o -type f -name '*.go' -print | wc -l)"
printf 'baseline.go_packages_non_spike=%s\n' "$(wc -l <"$non_spike_packages")"
printf 'baseline.internal_import_edges_non_spike=%s\n' "$(wc -l <"$internal_edges")"
printf 'baseline.helm_template_files=%s\n' \
  "$(rg -l -F '{{' charts/chill/templates | wc -l)"
printf 'baseline.helm_default_resources=%s\n' "$(wc -l <"$default_resources")"
printf 'baseline.helm_profiler_resources=%s\n' "$(wc -l <"$profiler_resources")"

while IFS= read -r resource; do
  printf 'baseline.helm_profiler_only=%s\n' "$resource"
done < <(comm -13 "$default_resources" "$profiler_resources")

relevant_changes="$(git status --porcelain -- '*.go' go.mod go.sum charts/chill)"
if [[ -z "$relevant_changes" ]]; then
  printf 'baseline.relevant_worktree=clean\n'
else
  printf 'baseline.relevant_worktree=dirty\n'
  while IFS= read -r change; do
    printf 'baseline.relevant_change=%s\n' "$change"
  done <<<"$relevant_changes"
fi
