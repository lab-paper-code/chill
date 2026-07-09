#!/usr/bin/env bash
set -euo pipefail

command="${1:-help}"

helm_bin="${HELM:-helm}"
kubectl_bin="${KUBECTL:-kubectl}"
kubeconform_bin="${KUBECONFORM:-bin/kubeconform}"
kubeconform_flags="${KUBECONFORM_FLAGS:--strict -summary -kubernetes-version 1.31.0 -skip CustomResourceDefinition}"

release_name="${HELM_RELEASE:-chill}"
release_namespace="${HELM_NAMESPACE:-chill-system}"
chart="${HELM_CHART:-charts/chill}"
timeout="${HELM_TIMEOUT:-2m}"
values_file="${HELM_VALUES:-}"
extra_helm_set="${HELM_SET:-}"
controller_image="${CONTROLLER_IMG:-}"
node_discovery_image="${NODE_DISCOVERY_IMG:-}"

usage() {
	cat <<EOF
Usage: $0 <command>

Commands:
  preflight             Validate chart rendering and CRD ownership.
  install               Install or upgrade CHILL using the runtime selected by Helm values.
  start                 Start runtime components in dependency order.
  stop                  Stop runtime components in cleanup order.
  uninstall             Stop runtime components, uninstall the release, keep CRDs.
  purge-crds             Delete CHILL CRDs; requires CONFIRM_PURGE_CRDS=${release_name}.

Environment:
  HELM_RELEASE           Helm release name. Default: chill
  HELM_NAMESPACE         Helm release namespace. Default: chill-system
  HELM_CHART             Chart path. Default: charts/chill
  HELM_VALUES            Optional values file.
  HELM_SET               Extra Helm --set arguments, for simple unquoted flags.
  CONTROLLER_IMG         Optional controller image repository:tag.
  NODE_DISCOVERY_IMG     Optional node-discovery image repository:tag.
EOF
}

values_args=()
if [[ -n "${values_file}" ]]; then
	values_args+=("-f" "${values_file}")
fi

extra_args=()
if [[ -n "${extra_helm_set}" ]]; then
	# Intentionally supports simple flag lists such as:
	# HELM_SET='--set foo=bar --set baz=qux'
	extra_args=(${extra_helm_set})
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

helm_upgrade() {
	local phase="$1"
	shift
	helm_upgrade_args "${phase}" "--wait" "$@"
}

helm_upgrade_reuse() {
	local phase="$1"
	shift
	helm_upgrade_args "${phase}" "--reuse-values" "--wait" "$@"
}

helm_upgrade_reuse_nowait() {
	local phase="$1"
	shift
	helm_upgrade_args "${phase}" "--reuse-values" "$@"
}

helm_upgrade_args() {
	local phase="$1"
	shift
	echo "==> ${phase}"
	"${helm_bin}" upgrade --install "${release_name}" "${chart}" \
		--namespace "${release_namespace}" \
		--create-namespace \
		"${values_args[@]}" \
		"$@" \
		"${extra_args[@]}" \
		--timeout "${timeout}"
}

render_validate() {
	local out
	out="$(mktemp --suffix=.yaml)"
	"${helm_bin}" lint "${chart}" "${values_args[@]}"
	"${helm_bin}" template "${release_name}" "${chart}" \
		--namespace "${release_namespace}" \
		"${values_args[@]}" \
		>"${out}"
	if [[ -x "${kubeconform_bin}" ]]; then
		"${kubeconform_bin}" ${kubeconform_flags} "${out}"
	else
		echo "skip kubeconform: ${kubeconform_bin} is not executable" >&2
	fi
	rm -f "${out}"
}

crd_check() {
	RELEASE_NAME="${release_name}" \
	RELEASE_NAMESPACE="${release_namespace}" \
	CRD_DIR=config/crd/bases \
	KUBECTL="${kubectl_bin}" \
		./hack/helm-crd-ownership.sh check
}

release_exists() {
	"${helm_bin}" status "${release_name}" --namespace "${release_namespace}" >/dev/null 2>&1
}

require_release() {
	if release_exists; then
		return
	fi
	echo "Helm release ${release_name}/${release_namespace} is not installed. Run helm-install first." >&2
	exit 1
}

preflight() {
	render_validate
	crd_check
}

release_system_status_name() {
	local status_name=""
	if release_exists; then
		status_name="$("${helm_bin}" get manifest "${release_name}" --namespace "${release_namespace}" |
			awk -F'--system-status-name=' 'NF > 1 {print $2; exit}' |
			xargs)"
	fi
	if [[ -z "${status_name}" ]]; then
		status_name="${release_name}"
	fi
	printf "%s\n" "${status_name}"
}

cleanup_system_status() {
	local status_name="$1"
	if [[ -z "${status_name}" ]]; then
		return
	fi
	if ! "${kubectl_bin}" get crd chillsystems.edge.dacs.io >/dev/null 2>&1; then
		echo "skip ChillSystem cleanup: CRD chillsystems.edge.dacs.io is not installed"
		return
	fi
	echo "==> cleanup-system-status"
	"${kubectl_bin}" delete chillsystem.edge.dacs.io "${status_name}" \
		--namespace "${release_namespace}" \
		--ignore-not-found
}

install_base() {
	local args=(
		"--set" "crds.enabled=true"
	)
	append_image_values args controller "${controller_image}"
	append_image_values args nodeDiscovery "${node_discovery_image}"
	helm_upgrade "install" "${args[@]}"
}

controller_up() {
	require_release
	local args=(
		"--set" "crds.enabled=true"
		"--set" "controller.replicaCount=1"
		"--set" "nodeDiscovery.enabled=false"
	)
	append_image_values args controller "${controller_image}"
	helm_upgrade_reuse "controller-up" "${args[@]}"
}

discovery_up() {
	require_release
	controller_up
	local args=(
		"--set" "crds.enabled=true"
		"--set" "controller.replicaCount=1"
		"--set" "nodeDiscovery.enabled=true"
	)
	append_image_values args controller "${controller_image}"
	append_image_values args nodeDiscovery "${node_discovery_image}"
	helm_upgrade_reuse "discovery-up" "${args[@]}"
}

discovery_down() {
	require_release
	helm_upgrade_reuse_nowait "discovery-down" \
		--set crds.enabled=true \
		--set nodeDiscovery.enabled=false
}

controller_down() {
	require_release
	helm_upgrade_reuse "controller-down" \
		--set crds.enabled=true \
		--set controller.replicaCount=0 \
		--set nodeDiscovery.enabled=false
}

start_release() {
	discovery_up
}

stop_release() {
	if release_exists; then
		discovery_down
		controller_down
	fi
}

uninstall_release() {
	local status_name
	status_name="$(release_system_status_name)"
	stop_release
	echo "==> uninstall"
	"${helm_bin}" uninstall "${release_name}" \
		--namespace "${release_namespace}" \
		--ignore-not-found \
		--cascade foreground \
		--wait \
		--timeout "${timeout}"
	cleanup_system_status "${status_name}"
}

purge_crds() {
	if [[ "${CONFIRM_PURGE_CRDS:-}" != "${release_name}" ]]; then
		echo "Refusing to delete CRDs. Set CONFIRM_PURGE_CRDS=${release_name} to continue." >&2
		exit 1
	fi
	if release_exists; then
		echo "Refusing to delete CRDs while Helm release ${release_name}/${release_namespace} is installed. Run helm-uninstall first." >&2
		exit 1
	fi
	"${kubectl_bin}" delete -f config/crd/bases --ignore-not-found
}

case "${command}" in
preflight)
	preflight
	;;
install)
	preflight
	install_base
	;;
start)
	start_release
	;;
stop)
	stop_release
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
