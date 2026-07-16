#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
binary="${CHILL_CODEGRAPH:-$repo_root/bin/codegraph}"
expected_version="${CHILL_CODEGRAPH_VERSION:-v1.4.1}"
expected_version="${expected_version#v}"

if [[ ! -x "$binary" ]]; then
  echo "codegraph: missing repository-local binary; run 'make codegraph'" >&2
  exit 1
fi

export DO_NOT_TRACK=1
export CODEGRAPH_TELEMETRY=0
export CODEGRAPH_NO_UPDATE_CHECK=1
export CODEGRAPH_NO_DOWNLOAD=1

actual_version="$($binary --version)"
if [[ "$actual_version" != "$expected_version" ]]; then
  echo "codegraph: expected $expected_version, found $actual_version" >&2
  exit 1
fi

cd "$repo_root"
exec "$binary" "$@"
