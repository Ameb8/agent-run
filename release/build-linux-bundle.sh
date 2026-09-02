#!/usr/bin/env bash
# Build offline Linux bundles, one native binary and OCI archive per architecture.
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
version="${1:?usage: release/build-linux-bundle.sh <version> [output-directory]}"
output_dir="${2:-${repo_dir}/dist}"
case "${version}" in *[!0-9A-Za-z._+-]*|'') echo "release version contains unsupported characters" >&2; exit 2 ;; esac

stage="$(mktemp -d)"
trap 'rm -rf -- "${stage}"' EXIT
runtime_dir="${stage}/runtime"
# Build both artifacts in one release invocation; runtime/build.sh verifies each
# OCI index digest against the architecture-specific manifest entry.
"${repo_dir}/runtime/build.sh" "${runtime_dir}" amd64 arm64
manifest_digest="sha256:$(sha256sum "${repo_dir}/internal/runtime/manifest.json" | awk '{print $1}')"
mkdir -p "${output_dir}"
for architecture in amd64 arm64; do
  bundle_name="agentrun-${version}-linux-${architecture}"
  bundle_dir="${stage}/${bundle_name}"
  mkdir -p "${bundle_dir}/bin" "${bundle_dir}/share/agentrun"
  archive="${runtime_dir}/agentrun-runtime-linux-${architecture}.oci.tar"
  [ -s "${archive}" ] || { echo "runtime build did not produce linux/${architecture} archive" >&2; exit 1; }
  cp "${archive}" "${bundle_dir}/share/agentrun/runtime.oci.tar"
  cp "${repo_dir}/internal/runtime/manifest.json" "${bundle_dir}/share/agentrun/runtime.json"
  printf '{"schema_version":1,"architecture":"%s","manifest_digest":"%s"}\n' "${architecture}" "${manifest_digest}" > "${bundle_dir}/share/agentrun/bundle.json"
  cp "${repo_dir}/release/install.sh" "${bundle_dir}/install.sh"
  chmod 0755 "${bundle_dir}/install.sh"
  (
    cd "${repo_dir}"
    CGO_ENABLED=0 GOOS=linux GOARCH="${architecture}" go build -trimpath \
      -ldflags "-s -w -X github.com/Ameb8/agent-run/internal/runtime.BuildVersion=${version}" \
      -o "${bundle_dir}/bin/agentrun" ./cmd/agentrun
  )
  chmod 0755 "${bundle_dir}/bin/agentrun"
  "${repo_dir}/release/verify-bundle.sh" "${bundle_dir}" "${architecture}"
  tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='UTC 1970-01-01' \
    -C "${stage}" -czf "${output_dir}/${bundle_name}.tar.gz" "${bundle_name}"
  printf 'wrote %s\n' "${output_dir}/${bundle_name}.tar.gz"
done
