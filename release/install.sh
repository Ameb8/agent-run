#!/usr/bin/env bash
# Install an unpacked native AgentRun bundle without contacting a registry.
set -euo pipefail

bundle_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
prefix="${HOME:-}/.local"
if [ "${1:-}" = "--prefix" ]; then prefix="${2:-}"; [ -n "${prefix}" ] || { echo "--prefix requires a directory" >&2; exit 2; }; [ "$#" = 2 ] || { echo "usage: install.sh [--prefix <directory>]" >&2; exit 2; }
elif [ "$#" != 0 ]; then echo "usage: install.sh [--prefix <directory>]" >&2; exit 2; fi
binary="${bundle_dir}/bin/agentrun"; archive="${bundle_dir}/share/agentrun/runtime.oci.tar"; manifest="${bundle_dir}/share/agentrun/runtime.json"; metadata="${bundle_dir}/share/agentrun/bundle.json"
for file in "${binary}" "${archive}" "${manifest}" "${metadata}"; do [ -f "${file}" ] || { echo "bundle is missing ${file#"${bundle_dir}/"}" >&2; exit 1; }; done
# Do this before Docker, binary execution, or prefix creation.
architecture="$(sed -n 's/.*"architecture":"\([^"]*\)".*/\1/p' "${metadata}")"; case "${architecture}" in amd64|arm64) ;; *) echo "bundle has an unsupported architecture" >&2; exit 1 ;; esac
host_arch="$(uname -m)"; case "${host_arch}" in x86_64) host_arch=amd64 ;; aarch64) host_arch=arm64 ;; *) echo "unsupported host architecture: ${host_arch}" >&2; exit 1 ;; esac
[ "${host_arch}" = "${architecture}" ] || { echo "bundle is for linux/${architecture}, but this host is linux/${host_arch}" >&2; exit 1; }
manifest_digest="sha256:$(sha256sum "${manifest}" | awk '{print $1}')"; metadata_digest="$(sed -n 's/.*"manifest_digest":"\([^"]*\)".*/\1/p' "${metadata}")"
[ "${metadata_digest}" = "${manifest_digest}" ] || { echo "bundle runtime manifest is tampered or does not match bundle metadata" >&2; exit 1; }
expected_image_digest="$(awk -v arch="${architecture}" '$0 ~ "\"" arch "\"[[:space:]]*:" { in_arch=1; next } in_arch && /"image_digest"[[:space:]]*:/ { sub(/.*: *"/, ""); sub(/".*/, ""); print; exit } in_arch && /^[[:space:]]*}/ { exit }' "${manifest}")"
[ -n "${expected_image_digest}" ] || { echo "bundle runtime manifest lacks linux/${architecture} image digest" >&2; exit 1; }
archive_digest="$(tar -xOf "${archive}" index.json | sed -n 's/.*"digest":"\(sha256:[0-9a-f][0-9a-f]*\)".*/\1/p' | head -n1)"
[ "${archive_digest}" = "${expected_image_digest}" ] || { echo "bundle OCI archive is tampered or does not match linux/${architecture}" >&2; exit 1; }
# The embedded manifest is authoritative; checking it prevents a swapped
# external manifest from selecting a different archive/image identity.
identity="$("${binary}" version)"
printf '%s' "${identity}" | grep -F "\"image_digest\":\"${expected_image_digest}\"" >/dev/null || { echo "bundle runtime manifest does not match the embedded binary identity" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "Docker Engine is required" >&2; exit 1; }
# docker load consumes only this archive; there is no pull or registry lookup.
docker load --input "${archive}" >/dev/null
mkdir -p "${prefix}/bin" "${prefix}/share/agentrun"
install -m 0755 "${binary}" "${prefix}/bin/agentrun"
install -m 0644 "${manifest}" "${prefix}/share/agentrun/runtime.json"
install -m 0644 "${metadata}" "${prefix}/share/agentrun/bundle.json"
if ! "${prefix}/bin/agentrun" doctor; then echo "private runtime import or host prerequisites failed; installation was retained for diagnosis" >&2; exit 1; fi
printf 'installed AgentRun linux/%s in %s (add %s/bin to PATH)\n' "${architecture}" "${prefix}" "${prefix}"
