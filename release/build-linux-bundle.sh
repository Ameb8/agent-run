#!/usr/bin/env bash
# Build the installable, offline Linux/amd64 AgentRun distribution.
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
version="${1:?usage: release/build-linux-bundle.sh <version> [output-directory]}"
output_dir="${2:-${repo_dir}/dist}"

case "${version}" in
  *[!0-9A-Za-z._+-]*|'') echo "release version contains unsupported characters" >&2; exit 2 ;;
esac

stage="$(mktemp -d)"
trap 'rm -rf -- "${stage}"' EXIT
bundle_name="agentrun-${version}-linux-amd64"
bundle_dir="${stage}/${bundle_name}"
mkdir -p "${bundle_dir}/bin" "${bundle_dir}/share/agentrun"

# The OCI archive is produced before the binary so a release never assembles an
# image independently from the artifact it ships. runtime/build.sh also checks
# that the archive's OCI manifest digest is the one declared in runtime.json.
"${repo_dir}/runtime/build.sh" "${stage}/runtime"
cp "${stage}/runtime/agentrun-runtime.oci.tar" "${bundle_dir}/share/agentrun/runtime.oci.tar"
cp "${repo_dir}/internal/runtime/manifest.json" "${bundle_dir}/share/agentrun/runtime.json"
cp "${repo_dir}/release/install.sh" "${bundle_dir}/install.sh"
chmod 0755 "${bundle_dir}/install.sh"

(
  cd "${repo_dir}"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath -ldflags "-s -w -X github.com/Ameb8/agent-run/internal/runtime.BuildVersion=${version}" \
    -o "${bundle_dir}/bin/agentrun" ./cmd/agentrun
)
chmod 0755 "${bundle_dir}/bin/agentrun"

# This release check makes the external, shipped manifest and the manifest
# embedded in the executable a single identity. It uses no host pi or Node.
"${repo_dir}/release/verify-bundle.sh" "${bundle_dir}"

mkdir -p "${output_dir}"
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='UTC 1970-01-01' \
  -C "${stage}" -czf "${output_dir}/${bundle_name}.tar.gz" "${bundle_name}"
printf 'wrote %s\n' "${output_dir}/${bundle_name}.tar.gz"
