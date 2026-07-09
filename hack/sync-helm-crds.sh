#!/usr/bin/env bash
set -euo pipefail

src_dir="${1:-config/crd/bases}"
dst_dir="${2:-charts/chill-operator/templates/crds}"

mkdir -p "${dst_dir}"
find "${dst_dir}" -maxdepth 1 -type f -name '*.yaml' -delete

shopt -s nullglob
sources=("${src_dir}"/*.yaml)
if [ "${#sources[@]}" -eq 0 ]; then
	echo "no CRD YAML files found in ${src_dir}" >&2
	exit 1
fi

for src in "${sources[@]}"; do
	dst="${dst_dir}/$(basename "${src}")"
	{
		printf '{{- if .Values.crds.enabled }}\n'
		awk '
			/^  annotations:$/ {
				in_annotations = 1
				print
				next
			}
			in_annotations && /^    controller-gen\.kubebuilder\.io\/version:/ {
				print
				print "    helm.sh/resource-policy: keep"
				in_annotations = 0
				next
			}
			in_annotations && $0 !~ /^    / {
				print "    helm.sh/resource-policy: keep"
				in_annotations = 0
			}
			{ print }
			END {
				if (in_annotations) {
					print "    helm.sh/resource-policy: keep"
				}
			}
		' "${src}"
		printf '{{- end }}\n'
	} >"${dst}"
done
