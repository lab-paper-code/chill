#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
helm="${HELM:-helm}"
kubeconform="${KUBECONFORM:-$repo_root/bin/kubeconform}"
kubeconform_flags="${KUBECONFORM_FLAGS:--strict -summary -kubernetes-version 1.31.0 -skip CustomResourceDefinition,ChillSystem}"
container_tool="${CONTAINER_TOOL:-docker}"
kubediagrams_version="${KUBEDIAGRAMS_VERSION:-v0.8.0}"
kubediagrams_image="${KUBEDIAGRAMS_IMAGE:-docker.io/philippemerle/kubediagrams@sha256:e721f252097c5921f29f75615643c1ce5d649ba2811427d2dc89f7e18742aafa}"
helm_release="${HELM_RELEASE:-chill}"
helm_namespace="${HELM_NAMESPACE:-chill-system}"
helm_chart="${HELM_CHART:-charts/chill}"
output_root="${DEPLOYMENT_GRAPH_OUT:-$repo_root/bin/code-intelligence/deployment}"
kubediagrams_config="${KUBEDIAGRAMS_CONFIG:-$repo_root/config/code-intelligence/kubediagrams.yaml}"
expected_profiler_delta="$repo_root/hack/testdata/deployment-graph/profiler-only-resources.txt"

usage() {
  cat <<'EOF'
Usage: hack/deployment-graph.sh <default|profiler|all|check>

Generate ignored deployment-topology artifacts from validated CHILL Helm
renders. The diagram is a review view; manifest.yaml and resources.txt remain
the desired-state authority.
EOF
}

fail() {
  echo "deployment graph: $*" >&2
  exit 1
}

require_prerequisites() {
  command -v "$helm" >/dev/null 2>&1 || fail "Helm command not found: $helm"
  [[ -x "$kubeconform" ]] || fail "kubeconform not found: $kubeconform"
  command -v "$container_tool" >/dev/null 2>&1 || fail "container command not found: $container_tool"
  command -v jq >/dev/null 2>&1 || fail "jq is required"
  [[ -f "$kubediagrams_config" ]] || fail "KubeDiagrams config not found: $kubediagrams_config"
  [[ -f "$expected_profiler_delta" ]] || fail "Profiler resource oracle not found: $expected_profiler_delta"
  [[ "$kubediagrams_version" == v0.8.0 ]] || fail "expected KubeDiagrams v0.8.0, found $kubediagrams_version"
  [[ "$kubediagrams_image" == *@sha256:* ]] || fail "KubeDiagrams image must be pinned by digest"
}

ensure_image() {
  if ! "$container_tool" image inspect "$kubediagrams_image" >/dev/null 2>&1; then
    "$container_tool" pull "$kubediagrams_image"
  fi
}

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
  ' "$manifest" | LC_ALL=C sort -u >"$output"
}

render_profile() {
  local profile="$1"
  local profile_dir="$output_root/$profile"
  local manifest="$profile_dir/manifest.yaml"
  local resources="$profile_dir/resources.txt"
  local metadata="$profile_dir/metadata.json"
  local set_args_json='[]'
  local -a profile_args=()
  local -a validation_args=()

  case "$profile" in
    default)
      ;;
    profiler)
      profile_args=(--set profiler.enabled=true)
      set_args_json='["profiler.enabled=true"]'
      ;;
    *)
      fail "unknown profile: $profile"
      ;;
  esac

  mkdir -p "$profile_dir"
  rm -f "$manifest" "$resources" "$metadata" \
    "$profile_dir/diagram.svg" "$profile_dir/diagram.dot_json"

  "$helm" template "$helm_release" "$helm_chart" \
    --namespace "$helm_namespace" "${profile_args[@]}" >"$manifest"

  read -r -a validation_args <<<"$kubeconform_flags"
  "$kubeconform" "${validation_args[@]}" "$manifest"
  extract_resources "$manifest" "$resources"

  for output in diagram.svg diagram.dot_json; do
    local -a diagram_args=(
      run --rm
      --network none
      --read-only
      --tmpfs /tmp:rw
      --env HOME=/tmp
      --user "$(id -u):$(id -g)"
      --volume "$profile_dir:/work"
      --volume "$kubediagrams_config:/config/kubediagrams.yaml:ro"
      --workdir /work
      "$kubediagrams_image"
      kube-diagrams manifest.yaml
      --config /config/kubediagrams.yaml
      --output "$output"
    )
    if [[ "$output" == diagram.svg ]]; then
      diagram_args+=(--embed-all-icons)
    fi
    "$container_tool" "${diagram_args[@]}"
    [[ -s "$profile_dir/$output" ]] || fail "$profile $output is empty"
  done

  local chart_version
  local chart_app_version
  local chart_dirty=false
  local helm_version
  local manifest_sha256
  local resources_sha256
  local resource_count

  chart_version="$(awk '$1 == "version:" {gsub(/\"/, "", $2); print $2; exit}' "$helm_chart/Chart.yaml")"
  chart_app_version="$(awk '$1 == "appVersion:" {gsub(/\"/, "", $2); print $2; exit}' "$helm_chart/Chart.yaml")"
  if [[ -n "$(git -C "$repo_root" status --porcelain -- "$helm_chart")" ]]; then
    chart_dirty=true
  fi
  helm_version="$($helm version --short)"
  manifest_sha256="$(sha256sum "$manifest" | awk '{print $1}')"
  resources_sha256="$(sha256sum "$resources" | awk '{print $1}')"
  resource_count="$(wc -l <"$resources")"

  jq -n \
    --arg schemaVersion "chill.dacs.io/deployment-graph-metadata.v1alpha1" \
    --arg profile "$profile" \
    --arg commit "$(git -C "$repo_root" rev-parse HEAD)" \
    --argjson chartDirty "$chart_dirty" \
    --arg chart "$helm_chart" \
    --arg chartVersion "$chart_version" \
    --arg chartAppVersion "$chart_app_version" \
    --arg release "$helm_release" \
    --arg namespace "$helm_namespace" \
    --argjson setArgs "$set_args_json" \
    --arg helmVersion "$helm_version" \
    --arg kubeconformFlags "$kubeconform_flags" \
    --arg kubediagramsVersion "$kubediagrams_version" \
    --arg kubediagramsImage "$kubediagrams_image" \
    --arg manifestSha256 "$manifest_sha256" \
    --arg resourcesSha256 "$resources_sha256" \
    --argjson resourceCount "$resource_count" \
    '{
      schemaVersion: $schemaVersion,
      profile: $profile,
      git: {commit: $commit, chartDirty: $chartDirty},
      helm: {
        chart: $chart,
        chartVersion: $chartVersion,
        chartAppVersion: $chartAppVersion,
        release: $release,
        namespace: $namespace,
        setArgs: $setArgs,
        version: $helmVersion
      },
      validation: {kubeconformFlags: $kubeconformFlags},
      diagram: {
        role: "non-authoritative-review-view",
        kubediagramsVersion: $kubediagramsVersion,
        image: $kubediagramsImage,
        outputs: ["diagram.svg", "diagram.dot_json"]
      },
      inputs: {
        manifest: "manifest.yaml",
        manifestSha256: $manifestSha256,
        resources: "resources.txt",
        resourcesSha256: $resourcesSha256,
        resourceCount: $resourceCount
      }
    }' >"$metadata"

  printf 'deployment_graph.profile=%s\n' "$profile"
  printf 'deployment_graph.resources=%s\n' "$resource_count"
  printf 'deployment_graph.output=%s\n' "$profile_dir"
}

check_diagram_node() {
  local graph="$1"
  local kind="$2"
  local name="$3"

  jq -e --arg kind "$kind" --arg name "$name" '
    any(.objects[]?;
      ((.tooltip // "") | contains("kind: \($kind)\n")) and
      ((.tooltip // "") | contains("  name: \($name)\n"))
    )
  ' "$graph" >/dev/null
}

check_edge_text() {
  local graph="$1"
  local expected="$2"

  jq -e --arg expected "$expected" '
    any(.edges[]?; (.tooltip // "") | contains($expected))
  ' "$graph" >/dev/null
}

check_outputs() {
  local default_dir="$output_root/default"
  local profiler_dir="$output_root/profiler"
  local removed="$output_root/removed-resources.txt"
  local added="$output_root/added-resources.txt"

  comm -23 "$default_dir/resources.txt" "$profiler_dir/resources.txt" >"$removed"
  comm -13 "$default_dir/resources.txt" "$profiler_dir/resources.txt" >"$added"

  [[ ! -s "$removed" ]] || {
    cat "$removed" >&2
    fail "Profiler profile unexpectedly removes resources"
  }
  diff -u "$expected_profiler_delta" "$added" || fail "Profiler resource delta changed"
  ! grep -Fxq 'Deployment/chill-profiler' "$profiler_dir/resources.txt" || \
    fail "Helm must not own the Operator-managed Profiler Deployment"

  check_diagram_node "$default_dir/diagram.dot_json" ChillSystem chill || \
    fail "default diagram is missing ChillSystem/chill"
  check_diagram_node "$profiler_dir/diagram.dot_json" ChillSystem chill || \
    fail "Profiler diagram is missing ChillSystem/chill"

  while IFS=/ read -r kind name; do
    check_diagram_node "$profiler_dir/diagram.dot_json" "$kind" "$name" || \
      fail "Profiler diagram is missing $kind/$name"
  done <"$expected_profiler_delta"

  check_edge_text "$profiler_dir/diagram.dot_json" $'kind: ServiceAccount\n  name: chill-profiler' || \
    fail "Profiler diagram is missing its ServiceAccount binding edge"
  check_edge_text "$profiler_dir/diagram.dot_json" $'kind: Role\n  name: chill-profiler' || \
    fail "Profiler diagram is missing its Role binding edge"
  check_edge_text "$profiler_dir/diagram.dot_json" $'kind: ClusterRole\n  name: chill-profiler' || \
    fail "Profiler diagram is missing its ClusterRole binding edge"
  check_edge_text "$default_dir/diagram.dot_json" 'spec.nodeDiscovery.configMapName' || \
    fail "default diagram is missing the ChillSystem node-discovery config reference"
  check_edge_text "$default_dir/diagram.dot_json" 'spec.deviceDiscovery.catalog.name' || \
    fail "default diagram is missing the ChillSystem device-catalog reference"
  check_edge_text "$profiler_dir/diagram.dot_json" 'spec.profiler.configRef.name' || \
    fail "Profiler diagram is missing the ChillSystem config reference"

  jq -e --arg image "$kubediagrams_image" '
    .profile == "default" and
    .diagram.role == "non-authoritative-review-view" and
    .diagram.image == $image and
    .inputs.resourceCount > 0
  ' "$default_dir/metadata.json" >/dev/null
  jq -e --arg image "$kubediagrams_image" '
    .profile == "profiler" and
    .helm.setArgs == ["profiler.enabled=true"] and
    .diagram.role == "non-authoritative-review-view" and
    .diagram.image == $image and
    .inputs.resourceCount > 0
  ' "$profiler_dir/metadata.json" >/dev/null

  for profile in default profiler; do
    local profile_dir="$output_root/$profile"
    [[ "$(jq -r '.inputs.manifestSha256' "$profile_dir/metadata.json")" == \
      "$(sha256sum "$profile_dir/manifest.yaml" | awk '{print $1}')" ]] || \
      fail "$profile metadata has a stale manifest hash"
    [[ "$(jq -r '.inputs.resourcesSha256' "$profile_dir/metadata.json")" == \
      "$(sha256sum "$profile_dir/resources.txt" | awk '{print $1}')" ]] || \
      fail "$profile metadata has a stale resource hash"
  done

  printf 'deployment_graph.check.default_resources=%s\n' "$(wc -l <"$default_dir/resources.txt")"
  printf 'deployment_graph.check.profiler_resources=%s\n' "$(wc -l <"$profiler_dir/resources.txt")"
  printf 'deployment_graph.check.profiler_delta=pass:%s\n' "$(wc -l <"$added")"
  printf 'deployment_graph.check.chillsystem_references=pass:3\n'
  printf 'deployment_graph.check.profiler_rbac=pass\n'
  printf 'deployment_graph.check.profiler_deployment_owner=operator\n'
  printf 'deployment_graph.check.acceptance=pass-review-artifact-only\n'
}

main() {
  local command="${1:-}"

  case "$command" in
    default|profiler|all|check)
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac

  cd "$repo_root"
  require_prerequisites
  ensure_image

  case "$command" in
    default|profiler)
      render_profile "$command"
      ;;
    all)
      render_profile default
      render_profile profiler
      ;;
    check)
      render_profile default
      render_profile profiler
      check_outputs
      ;;
  esac
}

main "$@"
