# AgentRun private runtime image

This directory produces the OCI image bundled with an AgentRun Linux release.
The release-owned [`../internal/runtime/manifest.json`](../internal/runtime/manifest.json)
is the authoritative identity for the image, Pi, JavaScript, and built-in tools;
the Go host embeds that file. [`versions.env`](versions.env) holds only build-only
inputs such as architecture-specific base-image digests and Pi tarball integrity.
The Docker build context is this directory only, so workspace and repository
files are never sent to the image builder.

- Pi is `@earendil-works/pi-coding-agent` 0.74.0, the first release after
  Pi's upstream package-scope migration. It supplies the extension API and the
  `read`, `grep`, `write`, `edit`, and shell implementations needed by v1. Its
  tarball SRI digest is checked during the build.
- JavaScript is Node.js 22.14.0, pinned through an official Node image digest
  for each native platform (`linux/amd64` and `linux/arm64`).
  Pi 0.74.0 requires Node 20.6.0 or newer, so this is a compatible LTS runtime.
- Debian packages come solely from the dated snapshot in `versions.env`; the
  base image's mutable package-source entries are removed before installation.
  `bash` and `ripgrep` support Pi's shell and grep built-in tools.

The image writes the build inputs and observed executable versions to
`/usr/share/agentrun/runtime.json` and OCI labels. npm, npx, and corepack are
removed after the build, so an AgentRun run cannot install extension
dependencies. It also has no preexisting Pi configuration and sets Pi offline,
with update checks and telemetry disabled; the host-side adapter supplies its
fresh, per-run configuration. The image contains neither Docker nor a Docker
socket; only the host-side AgentRun adapter may communicate with Docker Engine.

Runtime identity uses separate architecture-qualified local tags and OCI
manifest digests, not a mutable multi-platform tag. `agentrun` selects exactly
the entry for its Linux host architecture and checks Docker's reported platform
before it creates a sandbox or starts subscription login.

Run `task runtime:build` to emit
`dist/agentrun-runtime-linux-amd64.oci.tar` and
`dist/agentrun-runtime-linux-arm64.oci.tar`, each with a digest sidecar. Run
`task runtime:verify` on a native host to build, inspect, and probe that host's
artifact with networking disabled. `ARCH=amd64` or `ARCH=arm64` selects an
explicit native target; ARM64 release qualification must run on native ARM64,
not qemu/binfmt emulation.
