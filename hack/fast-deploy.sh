#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

registry="${CHILL_FAST_REGISTRY:-155.230.35.213:5000}"
image_namespace="${CHILL_FAST_IMAGE_NAMESPACE:-chill}"
image_tag="${IMAGE_TAG:-}"
auto_dirty_tag="false"
operator_image="${OPERATOR_IMG:-}"
node_discovery_image="${NODE_DISCOVERY_IMG:-}"
platforms="${PLATFORMS:-linux/amd64,linux/arm64}"
push_mode="${CHILL_FAST_PUSH_MODE:-auto}"
pull_policy="${CHILL_FAST_PULL_POLICY:-Always}"

helm_bin="${HELM:-helm}"
kubectl_bin="${KUBECTL:-kubectl}"
release="${HELM_RELEASE:-chill}"
release_namespace="${HELM_NAMESPACE:-chill-system}"
chart="${HELM_CHART:-charts/chill}"
timeout="${HELM_TIMEOUT:-2m}"
values_file="${HELM_VALUES:-}"
extra_helm_set="${HELM_SET:-}"
container_tool="${CONTAINER_TOOL:-docker}"
buildx_builder="${BUILDX_BUILDER:-}"
containerd_namespace="${CHILL_FAST_CONTAINERD_NAMESPACE:-k8s.io}"
observe_attempts="${CHILL_FAST_OBSERVE_ATTEMPTS:-30}"

yes="${CHILL_FAST_YES:-false}"
dry_run="${CHILL_FAST_DRY_RUN:-false}"
skip_build="${CHILL_FAST_SKIP_BUILD:-false}"
skip_push="${CHILL_FAST_SKIP_PUSH:-false}"
skip_deploy="${CHILL_FAST_SKIP_DEPLOY:-false}"

usage() {
	cat <<EOF
Usage: $0 [options]

Build CHILL development images, push them to the internal registry, reset the
current Helm release, and print observation output.

Options:
  --registry HOST[:PORT]       Internal registry. Default: ${registry}
  --image-namespace NAME       Registry repository prefix. Default: ${image_namespace}
  --tag TAG                    Image tag. Default: sha-<git-sha>[-dirty-<UTC timestamp>]
  --platforms LIST             Buildx platforms. Default: ${platforms}
  --push-mode MODE             auto, docker, buildx, ctr-plain-http, or none. Default: ${push_mode}
  --helm-release NAME          Helm release. Default: ${release}
  --helm-namespace NAME        Helm namespace. Default: ${release_namespace}
  --timeout DURATION           Helm/kubectl wait timeout. Default: ${timeout}
  --yes                        Do not prompt before uninstall/install.
  --dry-run                    Print commands without running them.
  --skip-build                 Do not build images.
  --skip-push                  Do not push images.
  --skip-deploy                Do not uninstall/install; only build/push and observe.
  -h, --help                   Show this help.

Environment:
  OPERATOR_IMG                 Full operator image ref. Overrides registry/tag defaults.
  NODE_DISCOVERY_IMG           Full node-discovery image ref. Overrides registry/tag defaults.
  IMAGE_TAG                    Image tag when --tag is not provided.
  CONTAINER_TOOL               docker-compatible build tool. Default: docker
  BUILDX_BUILDER               Optional buildx builder for multi-platform builds.
  HELM_VALUES                  Optional Helm values file.
  HELM_SET                     Extra Helm --set flags.
  CHILL_FAST_CTR               ctr command for ctr-plain-http mode. Default: sudo -n ctr
  CHILL_FAST_OBSERVE_ATTEMPTS  2-second polling attempts for operator-created resources. Default: 30
EOF
}

is_true() {
	case "${1}" in
	1|true|TRUE|yes|YES|y|Y) return 0 ;;
	*) return 1 ;;
	esac
}

quote_cmd() {
	printf '%q ' "$@"
}

run() {
	printf '+ '
	quote_cmd "$@"
	printf '\n'
	if ! is_true "${dry_run}"; then
		"$@"
	fi
}

try() {
	printf '+ '
	quote_cmd "$@"
	printf '|| true\n'
	if ! is_true "${dry_run}"; then
		"$@" || true
	fi
}

require_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

split_image_ref() {
	local image="$1"
	local component="$2"
	if [[ "${image}" != *:* || "${image}" == *@* ]]; then
		echo "${component} image must be repository:tag, got ${image}" >&2
		exit 1
	fi
}

choose_push_mode() {
	if [[ "${push_mode}" != "auto" ]]; then
		printf '%s\n' "${push_mode}"
		return
	fi
	case "${registry}" in
	155.230.35.213:5000|localhost:5000|127.0.0.1:5000)
		printf '%s\n' "ctr-plain-http"
		;;
	*)
		printf '%s\n' "docker"
		;;
	esac
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--registry)
		registry="$2"
		shift 2
		;;
	--image-namespace)
		image_namespace="$2"
		shift 2
		;;
	--tag)
		image_tag="$2"
		shift 2
		;;
	--platforms)
		platforms="$2"
		shift 2
		;;
	--push-mode)
		push_mode="$2"
		shift 2
		;;
	--helm-release)
		release="$2"
		shift 2
		;;
	--helm-namespace)
		release_namespace="$2"
		shift 2
		;;
	--timeout)
		timeout="$2"
		shift 2
		;;
	--yes)
		yes="true"
		shift
		;;
	--dry-run)
		dry_run="true"
		shift
		;;
	--skip-build)
		skip_build="true"
		shift
		;;
	--skip-push)
		skip_push="true"
		shift
		;;
	--skip-deploy)
		skip_deploy="true"
		shift
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		echo "unknown option: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

if [[ "${registry}" == http://* || "${registry}" == https://* ]]; then
	echo "--registry must be HOST[:PORT], not a URL: ${registry}" >&2
	exit 1
fi

if [[ -z "${image_tag}" ]]; then
	git_sha="$(git rev-parse --short HEAD 2>/dev/null || true)"
	if [[ -z "${git_sha}" ]]; then
		echo "IMAGE_TAG or --tag is required outside a git checkout" >&2
		exit 1
	fi
	if [[ -n "$(git status --porcelain)" ]]; then
		image_tag="sha-${git_sha}-dirty-$(date -u +%Y%m%d%H%M%S)"
		auto_dirty_tag="true"
	else
		image_tag="sha-${git_sha}"
	fi
fi

operator_image="${operator_image:-${registry}/${image_namespace}/chill-operator:${image_tag}}"
node_discovery_image="${node_discovery_image:-${registry}/${image_namespace}/chill-node-discovery:${image_tag}}"
push_mode="$(choose_push_mode)"

split_image_ref "${operator_image}" "operator"
split_image_ref "${node_discovery_image}" "node-discovery"

operator_selector="app.kubernetes.io/instance=${release},app.kubernetes.io/component=operator"
node_discovery_selector="app.kubernetes.io/instance=${release},app.kubernetes.io/component=node-discovery"
if [[ -n "${extra_helm_set}" ]]; then
	helm_set="${extra_helm_set} --set operator.image.pullPolicy=${pull_policy} --set nodeDiscovery.image.pullPolicy=${pull_policy}"
else
	helm_set="--set operator.image.pullPolicy=${pull_policy} --set nodeDiscovery.image.pullPolicy=${pull_policy}"
fi

require_cmd git
require_cmd make
require_cmd "${container_tool}"
require_cmd "${helm_bin}"
require_cmd "${kubectl_bin}"

if [[ "${push_mode}" == "ctr-plain-http" ]]; then
	require_cmd ctr
fi

case "${push_mode}" in
docker|buildx|ctr-plain-http|none) ;;
*)
	echo "unsupported --push-mode: ${push_mode}" >&2
	exit 1
	;;
esac

if is_true "${skip_push}" && ! is_true "${skip_build}" &&
	[[ "${push_mode}" == "buildx" || "${push_mode}" == "ctr-plain-http" ]]; then
	echo "--skip-push cannot be combined with push mode ${push_mode}; use --push-mode none for a local-only build" >&2
	exit 1
fi

echo "==> fast-deploy configuration"
echo "release: ${release}"
echo "namespace: ${release_namespace}"
echo "chart: ${chart}"
echo "operator image: ${operator_image}"
echo "node-discovery image: ${node_discovery_image}"
echo "platforms: ${platforms}"
if [[ -n "${buildx_builder}" ]]; then
	echo "buildx builder: ${buildx_builder}"
fi
echo "push mode: ${push_mode}"
echo "pull policy: ${pull_policy}"

if is_true "${auto_dirty_tag}"; then
	echo "dirty worktree: using unique image tag ${image_tag}" >&2
elif [[ -n "$(git status --porcelain)" ]]; then
	echo "warning: git worktree is dirty; ${image_tag} may not uniquely identify the image contents" >&2
fi

echo "==> target"
try "${kubectl_bin}" config current-context
try "${kubectl_bin}" cluster-info
try "${helm_bin}" status "${release}" --namespace "${release_namespace}"
try "${kubectl_bin}" get crd chillsystems.edge.dacs.io

wait_for_daemonset_object() {
	local attempt
	if is_true "${dry_run}"; then
		return
	fi
	echo "==> wait-node-discovery-daemonset"
	for (( attempt = 1; attempt <= observe_attempts; attempt++ )); do
		if "${kubectl_bin}" -n "${release_namespace}" get daemonset \
			-l "${node_discovery_selector}" \
			-o name 2>/dev/null | grep -q .; then
			return
		fi
		sleep 2
	done
	echo "node-discovery DaemonSet did not appear after $((observe_attempts * 2))s" >&2
	return 1
}

build_with_docker() {
	run make docker-build-all \
		CONTAINER_TOOL="${container_tool}" \
		OPERATOR_IMG="${operator_image}" \
		NODE_DISCOVERY_IMG="${node_discovery_image}"
}

push_with_docker() {
	run make docker-push-all \
		CONTAINER_TOOL="${container_tool}" \
		OPERATOR_IMG="${operator_image}" \
		NODE_DISCOVERY_IMG="${node_discovery_image}"
}

buildx_args() {
	if [[ -n "${buildx_builder}" ]]; then
		printf '%s\n' "--builder" "${buildx_builder}"
	fi
}

buildx_push_one() {
	local dockerfile="$1"
	local image="$2"
	local args=()
	while IFS= read -r arg; do
		[[ -n "${arg}" ]] && args+=("${arg}")
	done < <(buildx_args)

	run "${container_tool}" buildx build \
		"${args[@]}" \
		--push \
		--platform="${platforms}" \
		--file "${dockerfile}" \
		--tag "${image}" \
		.
}

buildx_push() {
	buildx_push_one "build/docker/operator.Dockerfile" "${operator_image}"
	buildx_push_one "build/docker/node-discovery.Dockerfile" "${node_discovery_image}"
}

ctr_plain_http_build_push_one() {
	local name="$1"
	local dockerfile="$2"
	local image="$3"
	local tmpdir archive
	local args=()
	while IFS= read -r arg; do
		[[ -n "${arg}" ]] && args+=("${arg}")
	done < <(buildx_args)
	tmpdir="$(mktemp -d)"
	archive="${tmpdir}/${name}.oci.tar"

	run "${container_tool}" buildx build \
		"${args[@]}" \
		--platform="${platforms}" \
		--file "${dockerfile}" \
		--tag "${image}" \
		--output "type=oci,dest=${archive}" \
		.

	# shellcheck disable=SC2206
	local ctr_cmd=(${CHILL_FAST_CTR:-sudo -n ctr})
	run "${ctr_cmd[@]}" -n "${containerd_namespace}" images import \
		--all-platforms \
		--digests \
		--index-name "${image}" \
		"${archive}"
	run "${ctr_cmd[@]}" -n "${containerd_namespace}" images push \
		--plain-http \
		"${image}"
	rm -rf "${tmpdir}"
}

if ! is_true "${skip_build}"; then
	case "${push_mode}" in
	docker|none)
		echo "==> build"
		build_with_docker
		;;
	buildx)
		echo "==> build-and-push"
		buildx_push
		skip_push="true"
		;;
	ctr-plain-http)
		echo "==> build-and-push"
		ctr_plain_http_build_push_one "operator" "build/docker/operator.Dockerfile" "${operator_image}"
		ctr_plain_http_build_push_one "node-discovery" "build/docker/node-discovery.Dockerfile" "${node_discovery_image}"
		skip_push="true"
		;;
	esac
fi

if ! is_true "${skip_push}"; then
	case "${push_mode}" in
	docker)
		echo "==> push"
		push_with_docker
		;;
	none)
		echo "skip push: push mode is none"
		;;
	buildx|ctr-plain-http)
		echo "skip push: push already happened during build"
		;;
	esac
fi

if ! is_true "${skip_deploy}"; then
	if ! is_true "${yes}" && ! is_true "${dry_run}"; then
		if [[ ! -t 0 ]]; then
			echo "refusing destructive deploy without --yes because stdin is not interactive" >&2
			exit 1
		fi
		echo
		echo "This will delete and reinstall Helm release ${release} in namespace ${release_namespace}."
		read -r -p "Type '${release}/${release_namespace}' to continue: " answer
		if [[ "${answer}" != "${release}/${release_namespace}" ]]; then
			echo "aborted"
			exit 1
		fi
	fi

	flow_env=(
		"HELM=${helm_bin}"
		"KUBECTL=${kubectl_bin}"
		"HELM_RELEASE=${release}"
		"HELM_NAMESPACE=${release_namespace}"
		"HELM_CHART=${chart}"
		"HELM_TIMEOUT=${timeout}"
		"HELM_VALUES=${values_file}"
		"HELM_SET=${helm_set}"
		"OPERATOR_IMG=${operator_image}"
		"NODE_DISCOVERY_IMG=${node_discovery_image}"
	)

	echo "==> uninstall"
	run env "${flow_env[@]}" ./hack/helm-release-flow.sh uninstall

	echo "==> install"
	run env "${flow_env[@]}" "HELM_SKIP_PREFLIGHT=1" ./hack/helm-release-flow.sh install

	run "${kubectl_bin}" -n "${release_namespace}" rollout status deployment \
		-l "${operator_selector}" "--timeout=${timeout}"
	wait_for_daemonset_object
	run "${kubectl_bin}" -n "${release_namespace}" rollout status daemonset \
		-l "${node_discovery_selector}" "--timeout=${timeout}"
fi

echo "==> observation"
echo "commit: $(git rev-parse --short HEAD 2>/dev/null || printf unknown)"
echo "operator image: ${operator_image}"
echo "node-discovery image: ${node_discovery_image}"
try "${helm_bin}" status "${release}" --namespace "${release_namespace}"
try "${kubectl_bin}" get chillsystems.edge.dacs.io -A -o wide
try "${kubectl_bin}" -n "${release_namespace}" get deployments,daemonsets,pods -o wide
try "${kubectl_bin}" -n "${release_namespace}" get pods \
	-o 'custom-columns=NAME:.metadata.name,NODE:.spec.nodeName,IMAGE:.spec.containers[*].image,IMAGE_ID:.status.containerStatuses[*].imageID'
try "${kubectl_bin}" -n "${release_namespace}" rollout status deployment -l "${operator_selector}" "--timeout=${timeout}"
try "${kubectl_bin}" -n "${release_namespace}" rollout status daemonset -l "${node_discovery_selector}" "--timeout=${timeout}"
try "${kubectl_bin}" -n "${release_namespace}" get events --sort-by=.lastTimestamp
try "${kubectl_bin}" -n "${release_namespace}" logs -l "${operator_selector}" --all-containers=true --tail=120 --prefix
try "${kubectl_bin}" -n "${release_namespace}" logs -l "${node_discovery_selector}" --all-containers=true --tail=120 --prefix
