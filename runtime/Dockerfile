# syntax=docker/dockerfile:1.7
ARG NODE_IMAGE
FROM ${NODE_IMAGE}

ARG NODE_IMAGE
ARG PI_PACKAGE
ARG PI_VERSION
ARG PI_TARBALL_INTEGRITY
ARG NODE_VERSION
ARG DEBIAN_SNAPSHOT

# A dated archive prevents later Debian repository updates from changing the
# image. bash is used by Pi's shell tool and ripgrep backs Pi's grep tool.
RUN rm -f /etc/apt/sources.list /etc/apt/sources.list.d/* \
    && printf '%s\n' \
      "deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}/ bookworm main" \
      > /etc/apt/sources.list \
    && printf '%s\n' 'Acquire::Check-Valid-Until "false";' \
      > /etc/apt/apt.conf.d/99agentrun-snapshot \
    && apt-get update \
    && apt-get install --no-install-recommends --yes bash ripgrep \
    && rm -rf /var/lib/apt/lists/*

# PI_VERSION is an exact package version, never a dist-tag or semver range.
# Verify the published package bytes against the checked-in SRI value before
# installing. npm is build-time-only and is removed from the final image below.
RUN tarball="$(npm pack --ignore-scripts --silent "${PI_PACKAGE}@${PI_VERSION}")" \
    && test "${PI_TARBALL_INTEGRITY}" = "$(node -e 'const fs = require("node:fs"); const crypto = require("node:crypto"); process.stdout.write("sha512-" + crypto.createHash("sha512").update(fs.readFileSync(process.argv[1])).digest("base64"));' "${tarball}")" \
    && npm install --global --omit=dev --ignore-scripts --no-audit --no-fund "${tarball}" \
    && rm -f "${tarball}" \
    && test "$(pi --version 2>&1)" = "${PI_VERSION}" \
    && test "$(node --version)" = "v${NODE_VERSION}"

RUN mkdir -p /usr/share/agentrun \
    && printf '%s\n' \
      '{' \
      "  \"pi_package\": \"${PI_PACKAGE}\", " \
      "  \"pi_version\": \"${PI_VERSION}\", " \
      "  \"pi_tarball_integrity\": \"${PI_TARBALL_INTEGRITY}\", " \
      "  \"javascript_version\": \"$(node --version)\", " \
      "  \"node_image\": \"${NODE_IMAGE}\", " \
      "  \"debian_snapshot\": \"${DEBIAN_SNAPSHOT}\", " \
      "  \"bash_version\": \"$(bash --version | head -n1)\", " \
      "  \"ripgrep_version\": \"$(rg --version | head -n1)\"" \
      '}' > /usr/share/agentrun/runtime.json \
    && rm -f /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
    && rm -rf /usr/local/lib/node_modules/npm /root/.npm /root/.pi

LABEL org.opencontainers.image.title="AgentRun private runtime" \
      org.opencontainers.image.description="Pinned Pi and JavaScript runtime for AgentRun" \
      org.opencontainers.image.version="pi-${PI_VERSION}-node-${NODE_VERSION}" \
      io.agentrun.pi.package="${PI_PACKAGE}" \
      io.agentrun.pi.version="${PI_VERSION}" \
      io.agentrun.javascript.version="${NODE_VERSION}" \
      io.agentrun.debian.snapshot="${DEBIAN_SNAPSHOT}"

# The host-side adapter provides the per-run configuration.  These defaults
# ensure that invoking the bundled executable cannot perform Pi update or
# package-install network activity before that configuration is in place.
ENV PI_OFFLINE=1 \
    PI_SKIP_VERSION_CHECK=1 \
    PI_TELEMETRY=0

# The future Docker adapter supplies the per-run Pi arguments.
WORKDIR /workspace
CMD ["pi", "--help"]
