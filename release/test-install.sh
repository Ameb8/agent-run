#!/usr/bin/env bash
# Offline installer tests: no daemon, registry, Pi, Node, npm, or npx.
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf -- "${work}"' EXIT
fake_bin="${work}/fake-bin"
mkdir -p "${fake_bin}"
cat > "${fake_bin}/docker" <<'SH'
#!/usr/bin/env sh
printf '%s\n' "$*" >> "${AGENTRUN_TEST_DOCKER_CALLS}"
test "${AGENTRUN_TEST_DOCKER_LOAD_FAIL:-0}" != 1
test "$1" = load && test "$2" = --input && test -f "$3"
SH
chmod +x "${fake_bin}/docker"

make_bundle() {
  architecture="$1"; bundle="$2"
  case "${architecture}" in amd64) digest="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ;; arm64) digest="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" ;; esac
  mkdir -p "${bundle}/bin" "${bundle}/share/agentrun" "${bundle}/oci"
  cp "${repo_dir}/release/install.sh" "${bundle}/install.sh"; chmod +x "${bundle}/install.sh"
  printf '{\n  "schema_version": 2,\n  "images": {\n    "amd64": {\n      "image":"runtime-amd64",\n      "image_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"\n    },\n    "arm64": {\n      "image":"runtime-arm64",\n      "image_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"\n    }\n  },\n  "pi":{"package":"pi","version":"1.0.0"},\n  "javascript_version":"v22.0.0",\n  "built_in_tools":[{"name":"read","description":"read"}]\n}\n' > "${bundle}/share/agentrun/runtime.json"
  manifest_digest="sha256:$(sha256sum "${bundle}/share/agentrun/runtime.json" | awk '{print $1}')"
  printf '{"schema_version":1,"architecture":"%s","manifest_digest":"%s"}\n' "${architecture}" "${manifest_digest}" > "${bundle}/share/agentrun/bundle.json"
  printf '{"schemaVersion":2,"manifests":[{"digest":"%s"}]}' "${digest}" > "${bundle}/oci/index.json"
  tar -C "${bundle}/oci" -cf "${bundle}/share/agentrun/runtime.oci.tar" index.json; rm -rf "${bundle}/oci"
  cat > "${bundle}/bin/agentrun" <<SH
#!/usr/bin/env sh
case "\$1" in
version) printf '%s\\n' '{"agentrun_version":"test","pi_version":"1.0.0","javascript_version":"v22.0.0","image_digest":"${digest}"}' ;;
doctor) printf '%s\\n' '{"checks":[{"name":"subscription_auth","status":"MISSING","optional":true}]}' ; exit "\${AGENTRUN_TEST_DOCTOR_EXIT:-0}" ;;
*) exit 1 ;;
esac
SH
  chmod +x "${bundle}/bin/agentrun"
}

calls="${work}/docker-calls"; export AGENTRUN_TEST_DOCKER_CALLS="${calls}"; : > "${calls}"
case "$(uname -m)" in x86_64) native_arch=amd64; other_arch=arm64 ;; aarch64) native_arch=arm64; other_arch=amd64 ;; *) echo "unsupported test host architecture" >&2; exit 1 ;; esac
native_bundle="${work}/${native_arch}"; native_prefix="${work}/native-prefix"; make_bundle "${native_arch}" "${native_bundle}"
PATH="${fake_bin}:/usr/bin:/bin" "${native_bundle}/install.sh" --prefix "${native_prefix}" >/dev/null
test -x "${native_prefix}/bin/agentrun"; test -f "${native_prefix}/share/agentrun/runtime.json"
grep -Fx "load --input ${native_bundle}/share/agentrun/runtime.oci.tar" "${calls}" >/dev/null

# A clean bundle installation is ready before optional subscription login.
# The fixture's doctor report deliberately has only missing optional auth.
test -x "${native_prefix}/bin/agentrun"

# Wrong host must stop before Docker or prefix mutation.
: > "${calls}"; other_bundle="${work}/${other_arch}"; other_prefix="${work}/other-prefix"; make_bundle "${other_arch}" "${other_bundle}"
if PATH="${fake_bin}:/usr/bin:/bin" "${other_bundle}/install.sh" --prefix "${other_prefix}" >/dev/null 2>&1; then exit 1; fi
test ! -e "${other_prefix}"; test ! -s "${calls}"

# Tampered manifest/archive and missing archive stop before docker load.
for case_name in manifest archive missing; do
  bundle="${work}/${case_name}"; prefix="${work}/${case_name}-prefix"; make_bundle "${native_arch}" "${bundle}"; : > "${calls}"
  case "${case_name}" in
    manifest) printf '\n' >> "${bundle}/share/agentrun/runtime.json" ;;
    archive) mkdir "${bundle}/bad"; printf '{"manifests":[{"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}]}' > "${bundle}/bad/index.json"; tar -C "${bundle}/bad" -cf "${bundle}/share/agentrun/runtime.oci.tar" index.json ;;
    missing) rm "${bundle}/share/agentrun/runtime.oci.tar" ;;
  esac
  if PATH="${fake_bin}:/usr/bin:/bin" "${bundle}/install.sh" --prefix "${prefix}" >/dev/null 2>&1; then exit 1; fi
  test ! -e "${prefix}"; test ! -s "${calls}"
done

# Failed image import has no fallback pull and no installed prefix.
: > "${calls}"; load_prefix="${work}/load-prefix"
if AGENTRUN_TEST_DOCKER_LOAD_FAIL=1 PATH="${fake_bin}:/usr/bin:/bin" "${native_bundle}/install.sh" --prefix "${load_prefix}" >/dev/null 2>&1; then exit 1; fi
test ! -e "${load_prefix}"
grep -Fx "load --input ${native_bundle}/share/agentrun/runtime.oci.tar" "${calls}" >/dev/null

# Required doctor failures still fail installation after the image is imported;
# retaining the prefix gives the user the installed doctor command for diagnosis.
: > "${calls}"; doctor_prefix="${work}/doctor-prefix"
if AGENTRUN_TEST_DOCTOR_EXIT=1 PATH="${fake_bin}:/usr/bin:/bin" "${native_bundle}/install.sh" --prefix "${doctor_prefix}" >/dev/null 2>&1; then exit 1; fi
test -x "${doctor_prefix}/bin/agentrun"
