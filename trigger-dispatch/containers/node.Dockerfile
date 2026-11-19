# Node.js 22 runtime for Gitslice bash automations (runtime: node).
FROM node:22-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      bash ca-certificates coreutils curl git jq openssh-client openssl zstd \
    && rm -rf /var/lib/apt/lists/* \
    && corepack enable && corepack prepare pnpm@latest --activate \
    && groupadd -g 10001 runner \
    && useradd -m -u 10001 -g 10001 -s /bin/bash runner \
    && mkdir -p /work /run/gitslice /run/secrets \
    && chown -R runner:runner /work /run/gitslice /run/secrets

COPY gs-exec /usr/local/bin/gs-exec
RUN chmod 0755 /usr/local/bin/gs-exec \
    && ln -s /usr/local/bin/gs-exec /usr/local/bin/gs-auto

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/gs-exec"]
