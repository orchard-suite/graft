# Graft image: the binary, the Ion engine, the Claude Code CLI,
# release-damnit, and the toolchains the repositories Graft works in build
# and test with (Go, Node, .NET, Python with uv, Rust). One image serves
# three containers: `ion serve` (the engine the agents run in), `graft
# workspace` (where the gate runs), and `graft` (the ports and the
# workers). The engine and the workspace need the toolchains: an agent
# builds and tests in one, and the gate re-runs the repository's check in
# the other. The engine's container also runs `graft tool` and `graft
# evidence`, the agent's handle, and `graft report`, from the same binary.
# The image holds the system libraries Chromium needs, and no browser: the
# workspace installs the one a repository's own Playwright wants. The
# workspace reads the files a person sends with poppler (a PDF's text, and
# its pages drawn as pictures) and tesseract (the words in every picture),
# inside the sandbox.
#
# Every package cache is under /cache: NuGet's packages, npm's cache,
# Playwright's browsers, Go's modules and builds, uv's and pip's caches. A
# deployment mounts one volume there, in the engine and the workspace
# alike, and keeps it across sandboxes so each request starts warm.
#
# Every version here is pinned. Bump deliberately.
ARG ENGINE_IMAGE=ghcr.io/dsswift/ion/engine:3.8.0
FROM ${ENGINE_IMAGE} AS engine

FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN VERSION=$(sed -E 's/ .*//' VERSION) \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/graft ./cmd/graft

FROM ubuntu:24.04
ARG GO_VERSION=1.27.2
ARG NODE_VERSION=24.22.0
ARG DOTNET_SDK_VERSION=10.0.402
ARG CLAUDE_CODE_VERSION=2.1.301
ARG RUST_VERSION=1.97.1
ARG UV_VERSION=0.13.5
ARG RELEASE_DAMNIT_VERSION=1.6.0
# Only for the system libraries Chromium needs: the browser itself is the
# one each repository's Playwright wants, installed at run time.
ARG PLAYWRIGHT_VERSION=1.65.1

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git bash make jq unzip xz-utils tini \
      build-essential pkg-config libicu74 \
      python3 python3-venv python3-pip \
      poppler-utils tesseract-ocr \
 && rm -rf /var/lib/apt/lists/*

# Go
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

# Node
RUN curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" | tar -C /usr/local --strip-components=1 -xJ

# The system libraries Chromium needs, for a prover that takes screenshots
# of the finished work with a repository's own Playwright setup. The
# browser is installed into the cache at run time, at the build that
# repository's Playwright version wants.
RUN npx -y "playwright@${PLAYWRIGHT_VERSION}" install-deps chromium \
 && rm -rf /var/lib/apt/lists/* /root/.npm

# .NET SDK
RUN curl -fsSL https://dot.net/v1/dotnet-install.sh -o /tmp/dotnet-install.sh \
 && bash /tmp/dotnet-install.sh --version "${DOTNET_SDK_VERSION}" --install-dir /usr/local/dotnet \
 && rm /tmp/dotnet-install.sh
ENV PATH="/usr/local/dotnet:${PATH}" DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1

# Rust. The toolchain is read-only at /usr/local/rustup; a deployment points
# CARGO_HOME at a writable cache for the registry.
ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo
ENV PATH="/usr/local/cargo/bin:${PATH}"
RUN curl -fsSL https://sh.rustup.rs | sh -s -- -y --no-modify-path --profile minimal \
      --default-toolchain "${RUST_VERSION}" --component rustfmt,clippy \
 && rustc --version

# uv, the Python project manager some repositories use.
RUN curl -fsSL "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-x86_64-unknown-linux-gnu.tar.gz" \
    | tar -C /usr/local/bin --strip-components=1 -xz uv-x86_64-unknown-linux-gnu/uv uv-x86_64-unknown-linux-gnu/uvx

# release-damnit: the commit-msg hook, the gate's commit check, and landing,
# for the repositories that version with it.
RUN curl -fsSL "https://github.com/dsswift/release-damnit/releases/download/v${RELEASE_DAMNIT_VERSION}/release-damnit-linux-amd64" \
      -o /usr/local/bin/release-damnit \
 && chmod +x /usr/local/bin/release-damnit

# Claude Code CLI, the engine's model backend. It hangs on any prompt under a
# bare virtual CPU model, such as kvm64; a host CPU model runs it.
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" && npm cache clean --force

# The package caches, under one folder a deployment may keep across
# sandboxes. Without a volume it is the container's own and starts empty.
ENV PLAYWRIGHT_BROWSERS_PATH=/cache/ms-playwright \
    NUGET_PACKAGES=/cache/nuget/packages \
    NUGET_HTTP_CACHE_PATH=/cache/nuget/http \
    npm_config_cache=/cache/npm \
    GOMODCACHE=/cache/go/mod \
    GOCACHE=/cache/go/build \
    UV_CACHE_DIR=/cache/uv \
    PIP_CACHE_DIR=/cache/pip
RUN mkdir -p /cache && chown 1000:1000 /cache

COPY --from=engine /ion /usr/local/bin/ion
COPY --from=build /out/graft /usr/local/bin/graft

# uid 1000 is the base image's `ubuntu` user.
RUN mkdir -p /data /work && chown 1000:1000 /data /work
USER 1000:1000
ENV HOME=/home/ubuntu \
    GRAFT_DATA_DIR=/data \
    GRAFT_WORK_DIR=/work
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["graft"]
