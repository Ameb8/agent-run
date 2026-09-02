#!/usr/bin/env bash
# Build release-owned OCI artifacts, one unambiguous tag and digest per Linux architecture.
set -euo pipefail

runtime_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd -- "${runtime_dir}/.." && pwd)"
output_dir="${1:-${repo_dir}/dist}"
if [ "$#" -gt 0 ]; then shift; fi
architectures=("$@")
if [ "${#architectures[@]}" = 0 ]; then architectures=(amd64 arm64); fi
source "${runtime_dir}/versions.env"
manifest="${repo_dir}/internal/runtime/manifest.json"
pi_package="$(sed -n 's/^[[:space:]]*"package":[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}")"
pi_version="$(sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}" | head -n1)"
javascript_version="$(sed -n 's/^[[:space:]]*"javascript_version":[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}")"
node_version="${javascript_version#v}"
mkdir -p "${output_dir}"

field_for_arch() {
  local arch="$1" field="$2"
  awk -v arch="$arch" -v field="$field" '
    $0 ~ "\"" arch "\"[[:space:]]*:" { inside=1; next }
    inside && $0 ~ "\"" field "\"[[:space:]]*:" { sub(/.*: *"/, ""); sub(/".*/, ""); print; exit }
    inside && $0 ~ /^[[:space:]]*}/ { exit }
  ' "${manifest}"
}

for architecture in "${architectures[@]}"; do
  case "${architecture}" in
    amd64) node_image="${NODE_IMAGE_AMD64}" ;;
    arm64) node_image="${NODE_IMAGE_ARM64}" ;;
    *) echo "unsupported runtime architecture: ${architecture}" >&2; exit 2 ;;
  esac
  image_tag="$(field_for_arch "${architecture}" image)"
  expected_digest="$(field_for_arch "${architecture}" image_digest)"
  test -n "${pi_package}" -a -n "${pi_version}" -a -n "${node_version}" -a -n "${image_tag}" -a -n "${expected_digest}"
  archive="${output_dir}/agentrun-runtime-linux-${architecture}.oci.tar"
  digest_file="${output_dir}/agentrun-runtime-linux-${architecture}.manifest.digest"
  rm -f "${archive}" "${digest_file}"
  docker buildx build --file "${runtime_dir}/Dockerfile" --platform "linux/${architecture}" --provenance=false --sbom=false --tag "${image_tag}" \
    --build-arg "NODE_IMAGE=${node_image}" --build-arg "NODE_VERSION=${node_version}" --build-arg "PI_PACKAGE=${pi_package}" --build-arg "PI_VERSION=${pi_version}" \
    --build-arg "PI_TARBALL_INTEGRITY=${PI_TARBALL_INTEGRITY}" --build-arg "DEBIAN_SNAPSHOT=${DEBIAN_SNAPSHOT}" \
    --output "type=oci,dest=${archive},tar=true,name=${image_tag}" "${runtime_dir}"
  tar -xOf "${archive}" index.json | grep -Eo 'sha256:[[:xdigit:]]{64}' | head -n1 > "${digest_file}"
  cmp -s "${digest_file}" <(printf '%s\n' "${expected_digest}") || { echo "built linux/${architecture} OCI digest does not match manifest" >&2; exit 1; }
  printf 'wrote %s (%s)\n' "${archive}" "$(cat "${digest_file}")"
done
