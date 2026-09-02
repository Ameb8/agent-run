# Linux release bundles

`release/build-linux-bundle.sh <version>` creates both
`dist/agentrun-<version>-linux-amd64.tar.gz` and
`dist/agentrun-<version>-linux-arm64.tar.gz`. Each archive contains exactly one
static native CLI, the matching private OCI archive, the authoritative runtime
manifest, bundle metadata, and an installer. The installer rejects a bundle for
the wrong host architecture before it calls Docker or creates the installation
prefix.

## Host requirements

AgentRun v1 requires 64-bit Linux (`x86_64` or `aarch64`), a non-rootless Linux
Docker Engine accessible to the invoking user, and Docker API 1.45 or newer.
It does not support 32-bit Raspberry Pi OS/ARMv7, macOS, Windows, rootless
Docker, or another container engine. A Raspberry Pi 4 needs a 64-bit OS and
enough free disk for Docker plus the bundled image; allow at least 4 GiB free
disk and 2 GiB RAM for practical installation and a small run. These are host
resources, not bundled dependencies.

Neither Pi, Node.js, npm, npx, nor registry access is required on the host.
Docker Engine is the one external prerequisite. The runtime archive is imported
with `docker load`; normal runs use `--pull=never` and cannot pull an image.

## Installation and checks

On a Raspberry Pi 4 (or another ARM64 Linux host), unpack the ARM64 archive and
run its installer:

```sh
tar -xzf agentrun-<version>-linux-arm64.tar.gz
./agentrun-<version>-linux-arm64/install.sh
~/.local/bin/agentrun version
~/.local/bin/agentrun doctor
```

Use the AMD64 archive equivalently on x86_64 Linux. `version` reports the
architecture-selected private-runtime digest. `doctor` verifies the local image
digest, Docker/API and sandbox prerequisites, bundled tools, and egress proxy
without making a model request. A `subscription_auth` check with
`"optional":true` is informational: a clean install without a subscription
credential passes doctor and can run `openai-compatible` agents. First install
or load the matching release bundle image, then pass the required doctor
checks, and only then, when running an `openai-subscription` agent, run
`agentrun auth login openai-subscription`. Authentication cannot install the
private image. Subscription presence is not credential validation; an
OpenAI-compatible smoke agent can use a controlled local/test provider
credential rather than a maintainer subscription.

For release qualification, use a clean native host of each architecture with
registry egress denied: install the matching bundle, run `version` and
`doctor`, inspect the loaded image platform, and run the normal controlled
provider smoke agent through `agentrun run`. Include a read-write smoke package
that makes a change beneath its selected workspace. ARM64 qualification must be
native ARM64 (for example a Pi 4 or native ARM64 runner), not qemu/binfmt.

`task release:check` exercises installer success plus wrong-host, missing
archive, tampered manifest/archive, and failed-load ordering without Docker or
a registry. `task runtime:verify` runs the native runtime probe selected by
`ARCH=amd64` or `ARCH=arm64`.
