#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"
if [[ "${1:-}" == run ]]; then
    shift
fi
exec python3 -B "${repo_root}/hack/deploy/main.py" "$@"
