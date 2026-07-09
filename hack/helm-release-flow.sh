#!/usr/bin/env bash
set -euo pipefail

command="${1:-help}"

helm_bin="${HELM:-helm}"
kubectl_bin="${KUBECTL:-kubectl}"
kubeconform_bin="${KUBECONFORM:-bin/kubeconform}"
kubeconform_flags="${KUBECONFORM_FLAGS:--strict -summary -kubernetes-version 1.31.0 -skip CustomResourceDefinition,ChillSystem}"

system_release="${HELM_RELEASE:-chill}"
operator_release="${HELM_OPERATOR_RELEASE:-chill-operator}"
release_namespace="${HELM_NAMESPACE:-chill-system}"
system_chart="${HELM_CHART:-charts/chill}"
operator_chart="${HELM_OPERATOR_CHART:-charts/chill-operator}"
timeout="${HELM_TIMEOUT:-2m}"
system_values_file="${HELM_VALUES:-}"
operator_values_file="${HELM_OPERATOR_VALUES:-}"
system_extra_set="${HELM_SET:-}"
operator_extra_set="${HELM_OPERATOR_SET:-}"
operator_image="${OPERATOR_IMG:-}"
node_discovery_image="${NODE_DISCOVERY_IMG:-}"

usage() {
	cat <<EOF
Usage: $0 <command>

Commands:
  preflight             Validate chart rendering and CRD ownership.
  install               Install or upgrade CHILL operator, then CHILL system.
  uninstall             Uninstall CHILL system, then CHILL operator.
  purge-crds            Delete CHILL CRDs; requires CONFIRM_PURGE_CRDS=${operator_release}.

Environment:
  HELM_OPERATOR_RELEASE  Operator Helm release name. Default: chill-operator
  HELM_RELEASE           CHILL system Helm release name. Default: chill
  HELM_NAMESPACE         Helm release namespace. Default: chill-system
  HELM_OPERATOR_CHART    Operator chart path. Default: charts/chill-operator
  HELM_CHART             System chart path. Default: charts/chill
  HELM_OPERATOR_VALUES   Optional operator values file.
  HELM_VALUES            Optional system values file.
  HELM_OPERATOR_SET      Extra operator Helm flags.
  HELM_SET               Extra system Helm flags.
  OPERATOR_IMG           Optional operator image repository:tag.
  NODE_DISCOVERY_IMG     Optional node-discovery image repository:tag.
EOF
}

operator_values_args=()
if [[ -n "${operator_values_file}" ]]; then
	operator_values_args+=("-f" "${operator_values_file}")
fi

system_values_args=()
if [[ -n "${system_values_file}" ]]; then
	system_values_args+=("-f" "${system_values_file}")
fi

operator_extra_args=()
if [[ -n "${operator_extra_set}" ]]; then
	operator_extra_args=(${operator_extra_set})
fi

system_extra_args=()
if [[ -n "${system_extra_set}" ]]; then
	system_extra_args=(${system_extra_set})
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
	trap 'rm -f "${out}"' EXIT
	local operator_args=("--set" "system.name=${system_release}")
	local system_args=("--set" "system.name=${system_release}")
	append_image_values operator_args operator "${operator_image}"
	append_image_values system_args nodeDiscovery "${node_discovery_image}"

	"${helm_bin}" lint "${operator_chart}" "${operator_values_args[@]}" "${operator_args[@]}" "${operator_extra_args[@]}"
	"${helm_bin}" lint "${system_chart}" "${system_values_args[@]}" "${system_args[@]}" "${system_extra_args[@]}"
	"${helm_bin}" template "${operator_release}" "${operator_chart}" \
		--namespace "${release_namespace}" \
		"${operator_values_args[@]}" \
		"${operator_args[@]}" \
		"${operator_extra_args[@]}" \
		>"${out}"
	"${helm_bin}" template "${system_release}" "${system_chart}" \
		--namespace "${release_namespace}" \
		"${system_values_args[@]}" \
		"${system_args[@]}" \
		"${system_extra_args[@]}" \
		>>"${out}"
	if [[ -x "${kubeconform_bin}" ]]; then
		"${kubeconform_bin}" ${kubeconform_flags} "${out}"
	else
		echo "skip kubeconform: ${kubeconform_bin} is not executable" >&2
	fi
}

crd_check() {
	RELEASE_NAME="${operator_release}" \
	RELEASE_NAMESPACE="${release_namespace}" \
	CRD_DIR=config/crd/bases \
	KUBECTL="${kubectl_bin}" \
		./hack/helm-crd-ownership.sh check
}

preflight() {
	render_validate
	crd_check
}

install_operator() {
	local args=("--set" "system.name=${system_release}")
	append_image_values args operator "${operator_image}"
	echo "==> install-operator"
	"${helm_bin}" upgrade --install "${operator_release}" "${operator_chart}" \
		--namespace "${release_namespace}" \
		--create-namespace \
		"${operator_values_args[@]}" \
		"${args[@]}" \
		"${operator_extra_args[@]}" \
		--wait \
		--timeout "${timeout}"
}

install_system() {
	local args=("--set" "system.name=${system_release}")
	append_image_values args nodeDiscovery "${node_discovery_image}"
	echo "==> install-system"
	"${helm_bin}" upgrade --install "${system_release}" "${system_chart}" \
		--namespace "${release_namespace}" \
		--create-namespace \
		"${system_values_args[@]}" \
		"${args[@]}" \
		"${system_extra_args[@]}" \
		--wait \
		--timeout "${timeout}"
}

install_release() {
	preflight
	install_operator
	install_system
}

uninstall_release() {
	echo "==> uninstall-system"
	"${helm_bin}" uninstall "${system_release}" \
		--namespace "${release_namespace}" \
		--ignore-not-found \
		--cascade foreground \
		--wait \
		--timeout "${timeout}"
	echo "==> uninstall-operator"
	"${helm_bin}" uninstall "${operator_release}" \
		--namespace "${release_namespace}" \
		--ignore-not-found \
		--cascade foreground \
		--wait \
		--timeout "${timeout}"
}

purge_crds() {
	if [[ "${CONFIRM_PURGE_CRDS:-}" != "${operator_release}" ]]; then
		echo "Refusing to delete CRDs. Set CONFIRM_PURGE_CRDS=${operator_release} to continue." >&2
		exit 1
	fi
	if "${helm_bin}" status "${system_release}" --namespace "${release_namespace}" >/dev/null 2>&1; then
		echo "Refusing to delete CRDs while Helm release ${system_release}/${release_namespace} is installed. Run helm-uninstall first." >&2
		exit 1
	fi
	if "${helm_bin}" status "${operator_release}" --namespace "${release_namespace}" >/dev/null 2>&1; then
		echo "Refusing to delete CRDs while Helm release ${operator_release}/${release_namespace} is installed. Run helm-uninstall first." >&2
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
