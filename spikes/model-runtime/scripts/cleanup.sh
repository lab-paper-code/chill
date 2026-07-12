#!/usr/bin/env bash
set -euo pipefail

kubectl delete namespace chill-model-spike --ignore-not-found --wait=true
