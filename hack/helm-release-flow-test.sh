#!/usr/bin/env bash

if [[ "${HELM_RELEASE_FLOW_TEST_HELM:-}" == "1" ]]; then
	printf '%s\n' "$@" >"${HELM_RELEASE_FLOW_TEST_CAPTURE:?}"
	exit 0
fi

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="${repo_root}/hack/helm-release-flow-test.sh"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "${temporary_directory}"' EXIT

values_file="${temporary_directory}/values.yaml"
printf '{}\n' >"${values_file}"
fake_kubeconform="${temporary_directory}/kubeconform"
printf '#!/usr/bin/env bash\nexit 0\n' >"${fake_kubeconform}"
chmod +x "${fake_kubeconform}"

values_only_capture="${temporary_directory}/values-only.args"
HELM_RELEASE_FLOW_TEST_HELM=1 \
	HELM_RELEASE_FLOW_TEST_CAPTURE="${values_only_capture}" \
	HELM_SKIP_PREFLIGHT=1 \
	make --no-print-directory -C "${repo_root}" helm-install \
		HELM="${script}" \
		KUBECONFORM="${fake_kubeconform}" \
		HELM_VALUES="${values_file}"

if ! grep -Fx -- "${values_file}" "${values_only_capture}" >/dev/null; then
	echo "values-only install did not retain HELM_VALUES" >&2
	exit 1
fi
if grep -Eq '^(operator|nodeDiscovery|nodeActuator|profiler|console)\.image\.(repository|tag)=' \
	"${values_only_capture}"; then
	echo "Makefile image defaults overrode HELM_VALUES" >&2
	exit 1
fi

explicit_capture="${temporary_directory}/explicit.args"
PROFILER_IMG="registry.example/chill/profiler:environment" \
	HELM_RELEASE_FLOW_TEST_HELM=1 \
	HELM_RELEASE_FLOW_TEST_CAPTURE="${explicit_capture}" \
	HELM_SKIP_PREFLIGHT=1 \
	make --no-print-directory -C "${repo_root}" helm-install \
		HELM="${script}" \
		KUBECONFORM="${fake_kubeconform}" \
		HELM_VALUES="${values_file}" \
		OPERATOR_IMG="registry.example/chill/operator:command-line"

for expected in \
	"operator.image.repository=registry.example/chill/operator" \
	"operator.image.tag=command-line" \
	"profiler.image.repository=registry.example/chill/profiler" \
	"profiler.image.tag=environment"; do
	if ! grep -Fx -- "${expected}" "${explicit_capture}" >/dev/null; then
		echo "explicit image override missing: ${expected}" >&2
		exit 1
	fi
done

component_image_values="$(
	grep -Ec '^(operator|nodeDiscovery|nodeActuator|profiler|console)\.image\.(repository|tag)=' \
		"${explicit_capture}"
)"
if [[ "${component_image_values}" != "4" ]]; then
	echo "explicit install projected unexpected component image values" >&2
	exit 1
fi

echo "helm-release-flow image precedence: pass"
