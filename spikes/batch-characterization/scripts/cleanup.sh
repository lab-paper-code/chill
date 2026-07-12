#!/usr/bin/env bash
# TODO(internal): Garbage collection is controller/owner-reference policy;
# production cleanup must not delete a shared namespace from a local script.
set -euo pipefail

kubectl delete namespace chill-batch-characterization --ignore-not-found
