#!/usr/bin/env bash
# This touches only the private image; it never consults host pi or Node.
set -euo pipefail

runtime_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd -- "${runtime_dir}/.." && pwd)"
image_tag="agentrun-runtime:verify"
source "${runtime_dir}/versions.env"
docker buildx build --load --file "${runtime_dir}/Dockerfile" --platform linux/amd64 \
  --provenance=false --sbom=false --tag "${image_tag}" \
  --build-arg "NODE_IMAGE=${NODE_IMAGE}" \
  --build-arg "NODE_VERSION=${NODE_VERSION}" \
  --build-arg "PI_PACKAGE=${PI_PACKAGE}" \
  --build-arg "PI_VERSION=${PI_VERSION}" \
  --build-arg "PI_TARBALL_INTEGRITY=${PI_TARBALL_INTEGRITY}" \
  --build-arg "DEBIAN_SNAPSHOT=${DEBIAN_SNAPSHOT}" "${runtime_dir}"

test "$(docker run --rm --network none "${image_tag}" pi --version)" = "${PI_VERSION}"
test "$(docker run --rm --network none "${image_tag}" node --version)" = "v${NODE_VERSION}"
docker run --rm --network none "${image_tag}" sh -ec '
  command -v bash
  command -v rg
  ! command -v npm
  ! command -v npx
  ! command -v corepack
  ! command -v yarn
  ! command -v pnpm
  ! command -v docker
  test ! -S /var/run/docker.sock
  test ! -e /root/.pi
  test -f /usr/share/agentrun/runtime.json
  test "$PI_OFFLINE" = 1
  test "$PI_SKIP_VERSION_CHECK" = 1
  test "$PI_TELEMETRY" = 0
'
docker run --rm --network none "${image_tag}" grep -F "\"pi_version\": \"${PI_VERSION}\"" /usr/share/agentrun/runtime.json
docker run --rm --network none "${image_tag}" grep -F "\"javascript_version\": \"v${NODE_VERSION}\"" /usr/share/agentrun/runtime.json

# A release-style OCI archive and its declared manifest must be available.
"${runtime_dir}/build.sh" "${repo_dir}/dist"
