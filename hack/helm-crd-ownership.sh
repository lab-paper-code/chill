#!/usr/bin/env bash
set -euo pipefail

mode="${1:-check}"
crd_dir="${CRD_DIR:-config/crd/bases}"
release_name="${RELEASE_NAME:-chill}"
release_namespace="${RELEASE_NAMESPACE:-chill-system}"
kubectl_bin="${KUBECTL:-kubectl}"
from_release_name="${FROM_RELEASE_NAME:-}"
from_release_namespace="${FROM_RELEASE_NAMESPACE:-}"

usage() {
	cat >&2 <<EOF
Usage: $0 check|adopt

Environment:
  CRD_DIR                 Directory containing generated CRD YAML files.
  RELEASE_NAME            Target Helm release name. Default: chill
  RELEASE_NAMESPACE       Target Helm release namespace. Default: chill-system
  KUBECTL                 kubectl binary. Default: kubectl
  FROM_RELEASE_NAME       Required by adopt when an existing CRD is owned by another Helm release.
  FROM_RELEASE_NAMESPACE  Required by adopt when an existing CRD is owned by another Helm release.
EOF
}

if [[ "${mode}" != "check" && "${mode}" != "adopt" ]]; then
	usage
	exit 2
fi

if [[ ! -d "${crd_dir}" ]]; then
	echo "CRD directory not found: ${crd_dir}" >&2
	exit 1
fi

crd_name_from_file() {
	local file="$1"
	awk '
		$0 == "metadata:" { in_metadata = 1; next }
		in_metadata && /^  name:/ { print $2; exit }
		in_metadata && /^[^ ]/ { in_metadata = 0 }
	' "${file}"
}

mapfile -t crds < <(
	shopt -s nullglob
	for file in "${crd_dir}"/*.yaml; do
		crd_name_from_file "${file}"
	done | sort
)

if [[ "${#crds[@]}" -eq 0 ]]; then
	echo "No CRD YAML files found in ${crd_dir}" >&2
	exit 1
fi

crd_field() {
	local crd="$1"
	local jsonpath="$2"
	"${kubectl_bin}" get crd "${crd}" -o "jsonpath=${jsonpath}" 2>/dev/null || true
}

crd_exists() {
	local crd="$1"
	local output
	if output="$("${kubectl_bin}" get crd "${crd}" -o name 2>&1)"; then
		return 0
	fi
	if [[ "${output}" == *"NotFound"* || "${output}" == *"not found"* ]]; then
		return 1
	fi
	echo "${output}" >&2
	exit 1
}

owned_by_target() {
	local release="$1"
	local namespace="$2"
	local managed_by="$3"
	[[ "${release}" == "${release_name}" && "${namespace}" == "${release_namespace}" && "${managed_by}" == "Helm" ]]
}

print_owner() {
	local release="$1"
	local namespace="$2"
	local managed_by="$3"
	printf 'release=%s namespace=%s managed-by=%s' "${release:-<none>}" "${namespace:-<none>}" "${managed_by:-<none>}"
}

check_crds() {
	local conflicts=0
	for crd in "${crds[@]}"; do
		if ! crd_exists "${crd}"; then
			echo "MISSING ${crd}: Helm can create it for ${release_name}/${release_namespace}"
			continue
		fi

		local current_release current_namespace managed_by
		current_release="$(crd_field "${crd}" '{.metadata.annotations.meta\.helm\.sh/release-name}')"
		current_namespace="$(crd_field "${crd}" '{.metadata.annotations.meta\.helm\.sh/release-namespace}')"
		managed_by="$(crd_field "${crd}" '{.metadata.labels.app\.kubernetes\.io/managed-by}')"

		if owned_by_target "${current_release}" "${current_namespace}" "${managed_by}"; then
			echo "OK ${crd}: owned by ${release_name}/${release_namespace}"
			continue
		fi

		conflicts=1
		echo "CONFLICT ${crd}: $(print_owner "${current_release}" "${current_namespace}" "${managed_by}")" >&2
	done

	if [[ "${conflicts}" -ne 0 ]]; then
		cat >&2 <<EOF

Existing CRDs are not owned by ${release_name}/${release_namespace}.
Use --set crds.enabled=false for an install-only smoke, or explicitly adopt
the CRDs after verifying that the old release should no longer own them.
EOF
		return 1
	fi
}

adopt_crds() {
	for crd in "${crds[@]}"; do
		if ! crd_exists "${crd}"; then
			echo "SKIP ${crd}: not present"
			continue
		fi

		local current_release current_namespace managed_by
		current_release="$(crd_field "${crd}" '{.metadata.annotations.meta\.helm\.sh/release-name}')"
		current_namespace="$(crd_field "${crd}" '{.metadata.annotations.meta\.helm\.sh/release-namespace}')"
		managed_by="$(crd_field "${crd}" '{.metadata.labels.app\.kubernetes\.io/managed-by}')"

		if owned_by_target "${current_release}" "${current_namespace}" "${managed_by}"; then
			echo "OK ${crd}: already owned by ${release_name}/${release_namespace}"
			continue
		fi

		if [[ -n "${current_release}${current_namespace}" ]]; then
			if [[ -z "${from_release_name}" || -z "${from_release_namespace}" ]]; then
				echo "REFUSE ${crd}: currently $(print_owner "${current_release}" "${current_namespace}" "${managed_by}"); set FROM_RELEASE_NAME and FROM_RELEASE_NAMESPACE to adopt" >&2
				exit 1
			fi
			if [[ "${current_release}" != "${from_release_name}" || "${current_namespace}" != "${from_release_namespace}" ]]; then
				echo "REFUSE ${crd}: expected ${from_release_name}/${from_release_namespace}, found $(print_owner "${current_release}" "${current_namespace}" "${managed_by}")" >&2
				exit 1
			fi
		elif [[ -n "${managed_by}" && "${managed_by}" != "Helm" ]]; then
			echo "REFUSE ${crd}: managed-by is ${managed_by}, not Helm" >&2
			exit 1
		fi

		"${kubectl_bin}" annotate crd "${crd}" \
			"meta.helm.sh/release-name=${release_name}" \
			"meta.helm.sh/release-namespace=${release_namespace}" \
			--overwrite
		"${kubectl_bin}" label crd "${crd}" app.kubernetes.io/managed-by=Helm --overwrite
		echo "ADOPTED ${crd}: ${release_name}/${release_namespace}"
	done
}

case "${mode}" in
check)
	check_crds
	;;
adopt)
	adopt_crds
	;;
esac
