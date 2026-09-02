#!/usr/bin/env bash
# Offline contract test for the installer. It uses a fake Docker client so no
# image registry, daemon, pi, Node, or model endpoint participates.
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf -- "${work}"' EXIT
bundle="${work}/bundle"
prefix="${work}/prefix"
mkdir -p "${bundle}/bin" "${bundle}/share/agentrun" "${work}/fake-bin"
cp "${repo_dir}/release/install.sh" "${bundle}/install.sh"
chmod +x "${bundle}/install.sh"
printf 'private image bytes' > "${bundle}/share/agentrun/runtime.oci.tar"
cat > "${bundle}/share/agentrun/runtime.json" <<'JSON'
{"schema_version":1,"image":"agentrun-runtime:private","image_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","pi":{"package":"pi","version":"1.0.0"},"javascript_version":"v22.0.0","built_in_tools":[{"name":"read","description":"read"}]}
JSON
cat > "${bundle}/bin/agentrun" <<'SH'
#!/usr/bin/env sh
case "$1" in
version) printf '%s\n' '{"agentrun_version":"test","pi_version":"1.0.0","javascript_version":"v22.0.0","image_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' ;;
doctor) exit 0 ;;
*) exit 1 ;;
esac
SH
chmod +x "${bundle}/bin/agentrun"
cat > "${work}/fake-bin/docker" <<SH
#!/usr/bin/env sh
printf '%s\n' "\$*" > "${work}/docker-call"
test "\$1" = load && test "\$2" = --input && test "\$3" = "${bundle}/share/agentrun/runtime.oci.tar"
SH
chmod +x "${work}/fake-bin/docker"

PATH="${work}/fake-bin:${PATH}" "${bundle}/install.sh" --prefix "${prefix}" >/dev/null
test -x "${prefix}/bin/agentrun"
test -f "${prefix}/share/agentrun/runtime.json"
grep -Fx "load --input ${bundle}/share/agentrun/runtime.oci.tar" "${work}/docker-call" >/dev/null
