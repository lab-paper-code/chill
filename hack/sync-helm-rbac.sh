#!/usr/bin/env bash
set -euo pipefail

source_file="config/rbac/role.yaml"
target_file="charts/chill/templates/_manager_rbac_rules.tpl"

{
  echo '{{- define "chill.managerRoleRules" -}}'
  awk '
    /^rules:$/ { in_rules = 1; next }
    in_rules { print }
  ' "${source_file}"
  echo '{{- end -}}'
} > "${target_file}"
