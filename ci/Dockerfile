# The image the Gitslice source's checks run in (.gitslice/checks.yaml,
# web/.gitslice/checks.yaml): Go and Node toolchains with the Go modules and
# the web app's npm dependencies installed. A CI daemon builds it from this
# file and the inputs the checks list (go.mod, go.sum, web/package*.json), and
# reuses it until one of them changes (design/31_ci_daemon.md).
FROM node:22-bookworm

ARG GO_VERSION=1.24.1
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" \
      | tar -C /usr/local -xz

# Checks run as the CI daemon's user, whoever that is on its machine, so
# everything they write goes to world-writable places: HOME, and /cache, where
# the checks mount their persistent Go build cache.
ENV PATH=/usr/local/go/bin:$PATH \
    GOPATH=/go \
    GOMODCACHE=/go/pkg/mod \
    GOCACHE=/cache/go-build \
    GOTOOLCHAIN=local \
    HOME=/home/ci
RUN mkdir -p /home/ci /cache/go-build && chmod 1777 /home/ci /cache /cache/go-build

# Dependencies: checks run without network, so everything they import is here.
WORKDIR /deps
COPY go.mod go.sum ./
RUN go mod download && chmod -R a+rX /go
COPY web/package.json web/package-lock.json web/
RUN cd web && npm ci --no-audit --no-fund && rm -rf /root/.npm
# tsc -b and vite keep their caches inside node_modules; let the check's user
# write them. Each run writes to its own container layer.
RUN cd web/node_modules && mkdir -p .tmp .vite && chmod 1777 . .tmp .vite

WORKDIR /workspace
