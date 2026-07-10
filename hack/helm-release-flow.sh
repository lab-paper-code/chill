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
system_name="${CHILL_SYSTEM_NAME:-}"

usage() {
	cat <<EOF
Usage: $0 <command>

Commands:
  preflight             Validate chart rendering and CRD ownership.
  install               Install or upgrade CHILL.
  uninstall             Delete CHILL runtime root, then uninstall Helm release while keeping CRDs.
  purge-crds            Delete CHILL CRDs; requires CONFIRM_PURGE_CRDS=${release}.

Environment:
  HELM_RELEASE           CHILL Helm release name. Default: chill
  HELM_NAMESPACE         Helm release namespace. Default: chill-system
  HELM_CHART             Chart path. Default: charts/chill
  HELM_VALUES            Optional values file.
  HELM_SET               Extra Helm flags.
  CHILL_SYSTEM_NAME      Optional ChillSystem name for cleanup. Default: discover by release labels, then HELM_RELEASE.
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

discover_chill_system_name() {
	if [[ -n "${system_name}" ]]; then
		printf '%s\n' "${system_name}"
		return
	fi
	if ! "${kubectl_bin}" get crd chillsystems.edge.dacs.io >/dev/null 2>&1; then
		printf '%s\n' "${release}"
		return
	fi

	local names
	names="$("${kubectl_bin}" get chillsystems.edge.dacs.io \
		-l "app.kubernetes.io/instance=${release},app.kubernetes.io/managed-by=Helm" \
		-o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' \
		--ignore-not-found 2>/dev/null || true)"

	local found=()
	while IFS= read -r name; do
		if [[ -n "${name}" ]]; then
			found+=("${name}")
		fi
	done <<<"${names}"

	if (( ${#found[@]} > 1 )); then
		echo "Multiple ChillSystem resources found for release ${release}: ${found[*]}" >&2
		echo "Set CHILL_SYSTEM_NAME to select one explicitly." >&2
		exit 1
	fi
	if (( ${#found[@]} == 1 )); then
		printf '%s\n' "${found[0]}"
		return
	fi
	printf '%s\n' "${release}"
}

delete_chill_runtime_root() {
	echo "==> chill-cleanup"
	if ! "${kubectl_bin}" get crd chillsystems.edge.dacs.io >/dev/null 2>&1; then
		echo "skip ChillSystem cleanup: CRD chillsystems.edge.dacs.io is not installed"
		return
	fi

	local name
	name="$(discover_chill_system_name)"
	if ! "${kubectl_bin}" get "chillsystems.edge.dacs.io/${name}" >/dev/null 2>&1; then
		echo "skip ChillSystem cleanup: ${name} not found"
		return
	fi

	"${kubectl_bin}" delete "chillsystems.edge.dacs.io/${name}" \
		--ignore-not-found=true \
		--wait=true \
		--timeout="${timeout}"
}

uninstall_release() {
	delete_chill_runtime_root
	echo "==> helm-uninstall"
	"${helm_bin}" uninstall "${release}" \
		--namespace "${release_namespace}" \
		--ignore-not-found \
		--no-hooks \
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
