# Cloud/ops runtime for Gitslice bash automations (runtime: cloud).
# Includes gcloud, awscli, kubectl, psql, gh, and terraform alongside base tools.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      bash ca-certificates coreutils curl git gnupg jq openssh-client openssl \
      postgresql-client python3 unzip zstd \
    && curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg \
      | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" \
      > /etc/apt/sources.list.d/google-cloud-sdk.list \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=amd64 signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
    && curl -fsSL https://apt.releases.hashicorp.com/gpg \
      | gpg --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] https://apt.releases.hashicorp.com bookworm main" \
      > /etc/apt/sources.list.d/hashicorp.list \
    && apt-get update && apt-get install -y --no-install-recommends \
      google-cloud-cli gh terraform \
    && curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip \
    && unzip -q /tmp/awscliv2.zip -d /tmp \
    && /tmp/aws/install \
    && rm -rf /tmp/aws /tmp/awscliv2.zip \
    && curl -fsSL "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" \
      -o /usr/local/bin/kubectl \
    && chmod 0755 /usr/local/bin/kubectl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 10001 runner \
    && useradd -m -u 10001 -g 10001 -s /bin/bash runner \
    && mkdir -p /work /run/gitslice /run/secrets \
    && chown -R runner:runner /work /run/gitslice /run/secrets

COPY gs-exec /usr/local/bin/gs-exec
RUN chmod 0755 /usr/local/bin/gs-exec \
    && ln -s /usr/local/bin/gs-exec /usr/local/bin/gs-auto

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/gs-exec"]
