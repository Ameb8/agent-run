#!/usr/bin/env bash
# Validate a staged bundle using only its own binary and artifacts.
set -euo pipefail

bundle_dir="${1:?usage: release/verify-bundle.sh <bundle-directory>}"
binary="${bundle_dir}/bin/agentrun"
manifest="${bundle_dir}/share/agentrun/runtime.json"
archive="${bundle_dir}/share/agentrun/runtime.oci.tar"
[ -x "${binary}" ] && [ -s "${manifest}" ] && [ -s "${archive}" ] || {
  echo "bundle is missing a required artifact" >&2; exit 1;
}

# The Go manifest parser is deliberately stricter than shell JSON handling;
# version exposes exactly its embedded identity. Compare release-owned fields
# without requiring jq, Python, Node, pi, or a network connection.
identity="$("${binary}" version)"
for field in agentrun_version pi_version javascript_version image_digest; do
  printf '%s' "${identity}" | grep -F "\"${field}\"" >/dev/null || {
    echo "bundle binary did not report ${field}" >&2; exit 1;
  }
done
for field in pi javascript_version image_digest; do
  case "${field}" in
    pi) value="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${manifest}" | head -n1)" ;;
    *) value="$(sed -n "s/.*\"${field}\": *\"\([^\"]*\)\".*/\1/p" "${manifest}")" ;;
  esac
  [ -n "${value}" ] || { echo "bundle manifest lacks ${field}" >&2; exit 1; }
  printf '%s' "${identity}" | grep -F "${value}" >/dev/null || {
    echo "bundle binary and runtime manifest disagree on ${field}" >&2; exit 1;
  }
done
