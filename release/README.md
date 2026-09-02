# Linux distribution bundle

`release/build-linux-bundle.sh <version>` creates
`dist/agentrun-<version>-linux-amd64.tar.gz`. The archive contains the static
Linux/amd64 CLI, the private OCI runtime archive, and the same authoritative
runtime manifest embedded in the CLI.

On a supported Linux host with Docker Engine, unpack it and run
`./agentrun-<version>-linux-amd64/install.sh [--prefix <directory>]`. The
installer uses `docker load` on the supplied archive only; it never pulls an
image. It then runs `version` and `doctor`, which verify the release identity,
the locally installed image, Docker isolation, and bundled tools without a
model request. Subscription authentication must be established separately if
the doctor policy requires it.

Release qualification is `task release:bundle` followed on a clean Linux Docker
host by installation, `agentrun version`, `agentrun doctor`, and the selected
representative package/run smoke test. The run must work with registry egress
blocked, because the runtime is already imported and `DockerSandbox` always
creates containers with `--pull=never`.
