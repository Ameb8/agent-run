#!/usr/bin/env bash
# Install an unpacked AgentRun Linux bundle without contacting a registry.
set -euo pipefail

bundle_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
prefix="${HOME:-}/.local"
if [ "${1:-}" = "--prefix" ]; then
  prefix="${2:-}"
  [ -n "${prefix}" ] || { echo "--prefix requires a directory" >&2; exit 2; }
  [ "$#" = 2 ] || { echo "usage: install.sh [--prefix <directory>]" >&2; exit 2; }
elif [ "$#" != 0 ]; then
  echo "usage: install.sh [--prefix <directory>]" >&2
  exit 2
fi

binary="${bundle_dir}/bin/agentrun"
archive="${bundle_dir}/share/agentrun/runtime.oci.tar"
manifest="${bundle_dir}/share/agentrun/runtime.json"
for file in "${binary}" "${archive}" "${manifest}"; do
  [ -f "${file}" ] || { echo "bundle is missing ${file#"${bundle_dir}/"}" >&2; exit 1; }
done
command -v docker >/dev/null 2>&1 || { echo "Docker Engine is required" >&2; exit 1; }

# docker load reads only the included archive. In particular, it cannot pull.
docker load --input "${archive}" >/dev/null

# Reject a bundle whose external manifest was swapped after assembly. The
# binary's embedded manifest is the release authority used by every run.
identity="$("${binary}" version)"
for field in image_digest javascript_version; do
  value="$(sed -n "s/.*\"${field}\": *\"\([^\"]*\)\".*/\1/p" "${manifest}")"
  [ -n "${value}" ] && printf '%s' "${identity}" | grep -F "\"${field}\":\"${value}\"" >/dev/null || {
    echo "bundle runtime manifest does not match the installed binary" >&2; exit 1;
  }
done
pi_version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}" | head -n1)"
[ -n "${pi_version}" ] && printf '%s' "${identity}" | grep -F "\"pi_version\":\"${pi_version}\"" >/dev/null || {
  echo "bundle runtime manifest does not match the installed binary" >&2; exit 1;
}

# The installed binary owns the parser and local-image verification policy.
# doctor is intentionally run after import; it performs no model request.
mkdir -p "${prefix}/bin" "${prefix}/share/agentrun"
install -m 0755 "${binary}" "${prefix}/bin/agentrun"
install -m 0644 "${manifest}" "${prefix}/share/agentrun/runtime.json"

if ! "${prefix}/bin/agentrun" version | grep -F "\"image_digest\"" >/dev/null; then
  echo "installed binary cannot report its bundled runtime identity" >&2
  exit 1
fi
if ! "${prefix}/bin/agentrun" doctor; then
  echo "private runtime import or host prerequisites failed; installation was retained for diagnosis" >&2
  exit 1
fi
printf 'installed AgentRun in %s (add %s/bin to PATH)\n' "${prefix}" "${prefix}"
