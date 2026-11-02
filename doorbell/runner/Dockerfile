# A Cloudflare-hosted CI runner (design/32_ci_dispatch_on_cloudflare.md,
# layer 2): Docker in Docker and gs. The doorbell Worker starts it when checks
# are queued for its daemon; it runs `gs agent start --ci` until it has been
# idle for a while, then exits. gs is built from this tree before a deploy
# (npm run build:runner).
FROM docker:28-dind

COPY gs /usr/local/bin/gs
COPY start.sh /usr/local/bin/start-runner
RUN chmod +x /usr/local/bin/gs /usr/local/bin/start-runner && mkdir -p /runner

WORKDIR /runner
ENTRYPOINT ["/usr/local/bin/start-runner"]
