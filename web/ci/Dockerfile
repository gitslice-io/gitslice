# The image the web app's checks run in (web/.gitslice/checks.yaml): Node with
# the app's npm dependencies installed in /deps/node_modules. A CI daemon builds
# it from this file and the package files, and reuses it until one of them
# changes (design/31_ci_daemon.md).
FROM node:22-bookworm

# Checks run as the CI daemon's user; give it a HOME it can write.
ENV HOME=/home/ci
RUN mkdir -p /home/ci && chmod 1777 /home/ci

WORKDIR /deps
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund && rm -rf /root/.npm
# tsc -b and vite keep their caches inside node_modules; let the check's user
# write them. Each run writes to its own container layer.
RUN cd node_modules && mkdir -p .tmp .vite && chmod 1777 . .tmp .vite

WORKDIR /workspace
