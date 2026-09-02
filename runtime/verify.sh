#!/usr/bin/env bash
# This touches only the private image; it never consults host pi or Node.
set -euo pipefail

runtime_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd -- "${runtime_dir}/.." && pwd)"
source "${runtime_dir}/versions.env"
manifest="${repo_dir}/internal/runtime/manifest.json"
architecture="${ARCH:-$(uname -m)}"
case "${architecture}" in x86_64|amd64) architecture=amd64; node_image="${NODE_IMAGE_AMD64}" ;; aarch64|arm64) architecture=arm64; node_image="${NODE_IMAGE_ARM64}" ;; *) echo "unsupported runtime architecture: ${architecture}" >&2; exit 2 ;; esac
pi_package="$(sed -n 's/^[[:space:]]*"package":[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}")"
pi_version="$(sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}")"
javascript_version="$(sed -n 's/^[[:space:]]*"javascript_version":[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}")"
image_tag="$(awk -v arch="${architecture}" '$0 ~ "\"" arch "\"[[:space:]]*:" { in_arch=1; next } in_arch && /"image"[[:space:]]*:/ { sub(/.*: *"/, ""); sub(/".*/, ""); print; exit }' "${manifest}")"
node_version="${javascript_version#v}"
test -n "${pi_package}" -a -n "${pi_version}" -a -n "${node_version}" -a -n "${image_tag}"
docker buildx build --load --file "${runtime_dir}/Dockerfile" --platform "linux/${architecture}" \
  --provenance=false --sbom=false --tag "${image_tag}" \
  --build-arg "NODE_IMAGE=${node_image}" \
  --build-arg "NODE_VERSION=${node_version}" \
  --build-arg "PI_PACKAGE=${pi_package}" \
  --build-arg "PI_VERSION=${pi_version}" \
  --build-arg "PI_TARBALL_INTEGRITY=${PI_TARBALL_INTEGRITY}" \
  --build-arg "DEBIAN_SNAPSHOT=${DEBIAN_SNAPSHOT}" "${runtime_dir}"

test "$(docker run --rm --network none "${image_tag}" pi --version)" = "${pi_version}"
test "$(docker run --rm --network none "${image_tag}" node --version)" = "${javascript_version}"
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
docker run --rm --network none "${image_tag}" grep -F "\"pi_version\": \"${pi_version}\"" /usr/share/agentrun/runtime.json
docker run --rm --network none "${image_tag}" grep -F "\"javascript_version\": \"${javascript_version}\"" /usr/share/agentrun/runtime.json

# A release-style OCI archive and its declared manifest must be available.
"${runtime_dir}/build.sh" "${repo_dir}/dist" "${architecture}"
