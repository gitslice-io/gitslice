# The image the Gitslice source's Go checks run in (.gitslice/checks.yaml):
# the Go toolchain with every module the build and tests need. A CI daemon
# builds it from this file, go.mod and go.sum, and reuses it until one of them
# changes (design/31_ci_daemon.md). The web app's checks have their own image
# (web/ci/Dockerfile).
FROM golang:1.24.1-bookworm

# Checks run as the CI daemon's user, whoever that is on its machine, so
# everything they write goes to world-writable places: HOME, and /cache, where
# the checks mount their persistent Go build cache.
ENV GOCACHE=/cache/go-build \
    GOTOOLCHAIN=local \
    HOME=/home/ci
RUN mkdir -p /home/ci /cache/go-build && chmod 1777 /home/ci /cache /cache/go-build

# Checks run without network, so every module they import is here.
WORKDIR /deps
COPY go.mod go.sum ./
RUN go mod download && chmod -R a+rX "$(go env GOMODCACHE)"

WORKDIR /workspace
