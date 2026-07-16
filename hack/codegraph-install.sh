#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
local_bin="${CHILL_LOCALBIN:-$repo_root/bin}"
version="${CHILL_CODEGRAPH_VERSION:-v1.4.1}"
platform="${CHILL_CODEGRAPH_PLATFORM:-linux-x64}"
expected_sha256="${CHILL_CODEGRAPH_SHA256:-fb585ff5018d6faaa46d282b61f4f689bc7967ed8a1b467a5c556dd7ced9b542}"

if [[ "$version" != "v1.4.1" || "$platform" != "linux-x64" ]]; then
  echo "codegraph install: no reviewed artifact for $version/$platform" >&2
  exit 1
fi

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) ;;
  *)
    echo "codegraph install: the Phase 3 pilot currently supports Linux/x86_64 only" >&2
    exit 1
    ;;
esac

install_dir="$local_bin/codegraph-$version"
binary="$install_dir/bin/codegraph"
link="$local_bin/codegraph"
expected_version="${version#v}"

mkdir -p "$local_bin"
if [[ -x "$binary" ]]; then
  actual_version="$(
    DO_NOT_TRACK=1 \
    CODEGRAPH_TELEMETRY=0 \
    CODEGRAPH_NO_UPDATE_CHECK=1 \
    CODEGRAPH_NO_DOWNLOAD=1 \
      "$binary" --version
  )"
  if [[ "$actual_version" != "$expected_version" ]]; then
    echo "codegraph install: expected $expected_version, found $actual_version" >&2
    exit 1
  fi
  ln -sfn "codegraph-$version/bin/codegraph" "$link"
  echo "codegraph.install=cached"
  echo "codegraph.version=$actual_version"
  exit 0
fi

asset="codegraph-$platform.tar.gz"
url="https://github.com/colbymchenry/codegraph/releases/download/$version/$asset"
temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT
archive="$temp_root/$asset"
stage="$temp_root/stage"
mkdir -p "$stage"

echo "Downloading CodeGraph $version for $platform"
curl -fL --retry 3 --output "$archive" "$url"
printf '%s  %s\n' "$expected_sha256" "$archive" | sha256sum --check
tar -xzf "$archive" -C "$stage" --strip-components=1

actual_version="$(
  DO_NOT_TRACK=1 \
  CODEGRAPH_TELEMETRY=0 \
  CODEGRAPH_NO_UPDATE_CHECK=1 \
  CODEGRAPH_NO_DOWNLOAD=1 \
    "$stage/bin/codegraph" --version
)"
if [[ "$actual_version" != "$expected_version" ]]; then
  echo "codegraph install: expected $expected_version, downloaded $actual_version" >&2
  exit 1
fi

rm -rf "$install_dir"
mv "$stage" "$install_dir"
ln -sfn "codegraph-$version/bin/codegraph" "$link"
echo "codegraph.install=downloaded"
echo "codegraph.version=$actual_version"
echo "codegraph.sha256=$expected_sha256"
