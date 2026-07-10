#!/usr/bin/env bash
set -euo pipefail

source_file="config/rbac/role.yaml"
target_file="charts/chill/templates/_operator_rbac_rules.tpl"

{
  echo '{{- define "chill.operatorRoleRules" -}}'
  awk '
    /^rules:$/ { in_rules = 1; next }
    in_rules { print }
  ' "${source_file}"
  echo '{{- end -}}'
} > "${target_file}"
