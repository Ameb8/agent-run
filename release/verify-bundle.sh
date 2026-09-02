#!/usr/bin/env bash
# Verify a staged bundle without Docker, a registry, pi, or Node.
set -euo pipefail

bundle_dir="${1:?usage: release/verify-bundle.sh <bundle-directory> <amd64|arm64>}"
architecture="${2:?usage: release/verify-bundle.sh <bundle-directory> <amd64|arm64>}"
case "${architecture}" in amd64|arm64) ;; *) echo "unsupported bundle architecture: ${architecture}" >&2; exit 2 ;; esac
binary="${bundle_dir}/bin/agentrun"
manifest="${bundle_dir}/share/agentrun/runtime.json"
archive="${bundle_dir}/share/agentrun/runtime.oci.tar"
metadata="${bundle_dir}/share/agentrun/bundle.json"
[ -x "${binary}" ] && [ -s "${manifest}" ] && [ -s "${archive}" ] && [ -s "${metadata}" ] || { echo "bundle is missing a required artifact" >&2; exit 1; }
metadata_arch="$(sed -n 's/.*"architecture":"\([^"]*\)".*/\1/p' "${metadata}")"
[ "${metadata_arch}" = "${architecture}" ] || { echo "bundle metadata architecture does not match bundle name" >&2; exit 1; }
manifest_digest="sha256:$(sha256sum "${manifest}" | awk '{print $1}')"
metadata_digest="$(sed -n 's/.*"manifest_digest":"\([^"]*\)".*/\1/p' "${metadata}")"
[ "${metadata_digest}" = "${manifest_digest}" ] || { echo "bundle manifest digest does not match bundle metadata" >&2; exit 1; }
expected_image_digest="$(awk -v arch="${architecture}" '$0 ~ "\"" arch "\"[[:space:]]*:" { in_arch=1; next } in_arch && /"image_digest"[[:space:]]*:/ { sub(/.*: *"/, ""); sub(/".*/, ""); print; exit } in_arch && /^[[:space:]]*}/ { exit }' "${manifest}")"
[ -n "${expected_image_digest}" ] || { echo "bundle manifest lacks linux/${architecture} image digest" >&2; exit 1; }
archive_digest="$(tar -xOf "${archive}" index.json | sed -n 's/.*"digest":"\(sha256:[0-9a-f][0-9a-f]*\)".*/\1/p' | head -n1)"
[ "${archive_digest}" = "${expected_image_digest}" ] || { echo "bundle OCI archive does not match linux/${architecture} manifest digest" >&2; exit 1; }
elf_machine="$(readelf -h "${binary}" | awk -F: '/Machine:/{gsub(/^[[:space:]]+/, "", $2); print $2}')"
case "${architecture}:${elf_machine}" in amd64:Advanced\ Micro\ Devices\ X86-64|arm64:AArch64) ;; *) echo "bundle binary architecture ${elf_machine:-unknown} does not match linux/${architecture}" >&2; exit 1 ;; esac
# A native executable also proves its embedded manifest selects this archive.
host_arch="$(uname -m)"; case "${host_arch}" in x86_64) host_arch=amd64 ;; aarch64) host_arch=arm64 ;; esac
if [ "${host_arch}" = "${architecture}" ]; then
  identity="$("${binary}" version)"
  printf '%s' "${identity}" | grep -F "\"image_digest\":\"${expected_image_digest}\"" >/dev/null || { echo "bundle binary and selected runtime manifest disagree on image digest" >&2; exit 1; }
fi
