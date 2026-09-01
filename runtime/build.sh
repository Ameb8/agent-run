#!/usr/bin/env bash
# Build a release-bundle OCI artifact and record its manifest digest.
set -euo pipefail

runtime_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd -- "${runtime_dir}/.." && pwd)"
output_dir="${1:-${repo_dir}/dist}"
source "${runtime_dir}/versions.env"
manifest="${repo_dir}/internal/runtime/manifest.json"
pi_package="$(sed -n 's/^[[:space:]]*"package":[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}")"
pi_version="$(sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}")"
javascript_version="$(sed -n 's/^[[:space:]]*"javascript_version":[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}")"
image_tag="$(sed -n 's/^[[:space:]]*"image":[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}")"
node_version="${javascript_version#v}"
test -n "${pi_package}" -a -n "${pi_version}" -a -n "${node_version}" -a -n "${image_tag}"
mkdir -p "${output_dir}"
archive="${output_dir}/agentrun-runtime.oci.tar"
digest_file="${output_dir}/agentrun-runtime.manifest.digest"

rm -f "${archive}" "${digest_file}"
docker buildx build --file "${runtime_dir}/Dockerfile" --platform linux/amd64 \
  --provenance=false --sbom=false \
  --tag "${image_tag}" \
  --build-arg "NODE_IMAGE=${NODE_IMAGE}" \
  --build-arg "NODE_VERSION=${node_version}" \
  --build-arg "PI_PACKAGE=${pi_package}" \
  --build-arg "PI_VERSION=${pi_version}" \
  --build-arg "PI_TARBALL_INTEGRITY=${PI_TARBALL_INTEGRITY}" \
  --build-arg "DEBIAN_SNAPSHOT=${DEBIAN_SNAPSHOT}" \
  --output "type=oci,dest=${archive},tar=true,name=${image_tag}" "${runtime_dir}"

# The OCI index records the content-addressed image manifest.
tar -xOf "${archive}" index.json | grep -Eo 'sha256:[[:xdigit:]]{64}' | head -n1 > "${digest_file}"
test -s "${digest_file}"
printf 'wrote %s (%s)\n' "${archive}" "$(cat "${digest_file}")"
