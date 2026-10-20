#!/usr/bin/env bash
# Fetch hourly observation segments from the evidence store's S3 endpoint.
# Usage: fetch.sh <from-hour> <to-hour> <dir>   hours as YYYYMMDDTHH (UTC), inclusive.
# It also fetches the hour before and after: power reads one minute past each
# end and the membership snapshot before the start.
# Needs the aws CLI, CHILL_S3_ENDPOINT, CHILL_S3_BUCKET and the store's
# AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY. Writes <dir>/obs/<hour>/<stream>.jsonl.gz.
set -euo pipefail
[[ $# -eq 3 ]] || { echo "usage: $0 <from YYYYMMDDTHH> <to YYYYMMDDTHH> <dir>" >&2; exit 2; }
: "${CHILL_S3_ENDPOINT:?}" "${CHILL_S3_BUCKET:?}"
to_epoch() { date -u -d "${1:0:8} ${1:9:2}:00" +%s; }
from=$(to_epoch "$1") to=$(to_epoch "$2")
for ((at = from - 3600; at <= to + 3600; at += 3600)); do
  hour=$(date -u -d "@$at" +%Y%m%dT%H)
  aws s3 cp --recursive --endpoint-url "$CHILL_S3_ENDPOINT" \
    "s3://$CHILL_S3_BUCKET/serving/observations.v1alpha5/$hour/" "$3/obs/$hour/"
done
