#!/usr/bin/env bash
set -euo pipefail

command="${1:-help}"

helm_bin="${HELM:-helm}"
kubectl_bin="${KUBECTL:-kubectl}"
kubeconform_bin="${KUBECONFORM:-bin/kubeconform}"
kubeconform_flags="${KUBECONFORM_FLAGS:--strict -summary -kubernetes-version 1.31.0 -skip CustomResourceDefinition,ChillSystem}"

release="${HELM_RELEASE:-chill}"
release_namespace="${HELM_NAMESPACE:-chill-system}"
chart="${HELM_CHART:-charts/chill}"
timeout="${HELM_TIMEOUT:-2m}"
values_file="${HELM_VALUES:-}"
extra_set="${HELM_SET:-}"
operator_image="${OPERATOR_IMG:-}"
node_discovery_image="${NODE_DISCOVERY_IMG:-}"

usage() {
	cat <<EOF
Usage: $0 <command>

Commands:
  preflight             Validate chart rendering and CRD ownership.
  install               Install or upgrade CHILL.
  uninstall             Uninstall CHILL while keeping CRDs.
  purge-crds            Delete CHILL CRDs; requires CONFIRM_PURGE_CRDS=${release}.

Environment:
  HELM_RELEASE           CHILL Helm release name. Default: chill
  HELM_NAMESPACE         Helm release namespace. Default: chill-system
  HELM_CHART             Chart path. Default: charts/chill
  HELM_VALUES            Optional values file.
  HELM_SET               Extra Helm flags.
  OPERATOR_IMG           Optional operator image repository:tag.
  NODE_DISCOVERY_IMG     Optional node-discovery image repository:tag.
EOF
}

values_args=()
if [[ -n "${values_file}" ]]; then
	values_args+=("-f" "${values_file}")
fi

extra_args=()
if [[ -n "${extra_set}" ]]; then
	extra_args=(${extra_set})
fi

split_image_ref() {
	local image="$1"
	local component="$2"
	if [[ "${image}" != *:* || "${image}" == *@* ]]; then
		echo "${component} image must be repository:tag, got ${image}" >&2
		exit 1
	fi
}

append_image_values() {
	local -n target_args="$1"
	local prefix="$2"
	local image="$3"
	if [[ -z "${image}" ]]; then
		return
	fi
	split_image_ref "${image}" "${prefix}"
	target_args+=("--set" "${prefix}.image.repository=${image%:*}")
	target_args+=("--set" "${prefix}.image.tag=${image##*:}")
}

render_validate() {
	local out
	out="$(mktemp --suffix=.yaml)"
	trap "rm -f '${out}'" EXIT
	local args=("--set" "system.name=${release}")
	append_image_values args operator "${operator_image}"
	append_image_values args nodeDiscovery "${node_discovery_image}"

	"${helm_bin}" lint "${chart}" "${values_args[@]}" "${args[@]}" "${extra_args[@]}"
	"${helm_bin}" template "${release}" "${chart}" \
		--namespace "${release_namespace}" \
		"${values_args[@]}" \
		"${args[@]}" \
		"${extra_args[@]}" \
		>"${out}"
	if [[ -x "${kubeconform_bin}" ]]; then
		"${kubeconform_bin}" ${kubeconform_flags} "${out}"
	else
		echo "skip kubeconform: ${kubeconform_bin} is not executable" >&2
	fi
}

crd_check() {
	RELEASE_NAME="${release}" \
	RELEASE_NAMESPACE="${release_namespace}" \
	CRD_DIR=config/crd/bases \
	KUBECTL="${kubectl_bin}" \
		./hack/helm-crd-ownership.sh check
}

preflight() {
	render_validate
	crd_check
}

install_release() {
	preflight
	local args=("--set" "system.name=${release}")
	append_image_values args operator "${operator_image}"
	append_image_values args nodeDiscovery "${node_discovery_image}"
	echo "==> install"
	"${helm_bin}" upgrade --install "${release}" "${chart}" \
		--namespace "${release_namespace}" \
		--create-namespace \
		"${values_args[@]}" \
		"${args[@]}" \
		"${extra_args[@]}" \
		--wait \
		--timeout "${timeout}"
}

uninstall_release() {
	echo "==> uninstall"
	"${helm_bin}" uninstall "${release}" \
		--namespace "${release_namespace}" \
		--ignore-not-found \
		--cascade foreground \
		--wait \
		--timeout "${timeout}"
}

purge_crds() {
	if [[ "${CONFIRM_PURGE_CRDS:-}" != "${release}" ]]; then
		echo "Refusing to delete CRDs. Set CONFIRM_PURGE_CRDS=${release} to continue." >&2
		exit 1
	fi
	if "${helm_bin}" status "${release}" --namespace "${release_namespace}" >/dev/null 2>&1; then
		echo "Refusing to delete CRDs while Helm release ${release}/${release_namespace} is installed. Run helm-uninstall first." >&2
		exit 1
	fi
	if "${kubectl_bin}" get crd chillsystems.edge.dacs.io >/dev/null 2>&1 &&
		[[ -n "$("${kubectl_bin}" get chillsystems.edge.dacs.io -o name --ignore-not-found 2>/dev/null)" ]]; then
		echo "Refusing to delete CRDs while ChillSystem resources still exist. Delete them and wait for finalizers first." >&2
		exit 1
	fi
	"${kubectl_bin}" delete -f config/crd/bases --ignore-not-found
}

case "${command}" in
preflight)
	preflight
	;;
install)
	install_release
	;;
uninstall)
	uninstall_release
	;;
purge-crds)
	purge_crds
	;;
help|-h|--help)
	usage
	;;
*)
	usage >&2
	exit 2
	;;
esac
