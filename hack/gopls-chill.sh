#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
gopls_bin="${CHILL_GOPLS_BIN:-$repo_root/bin/gopls}"
go_toolchain="${CHILL_GOPLS_GO_VERSION:-go1.26.4}"

if [[ ! -x "$gopls_bin" ]]; then
  printf 'missing repository-local gopls: run make gopls\n' >&2
  exit 1
fi

exec env GOTOOLCHAIN="$go_toolchain" "$gopls_bin" "$@"
