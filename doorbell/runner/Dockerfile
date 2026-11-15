# A Cloudflare-hosted CI runner (design/32_ci_dispatch_on_cloudflare.md,
# layer 2): Docker in Docker and gs. The doorbell Worker starts it when checks
# are queued for its daemon; it runs `gs agent start --ci` as an unprivileged
# user with container isolation enforced until idle, then exits. gs is built
# from this tree before a deploy (npm run build:runner).
FROM docker:28-dind

RUN apk add --no-cache su-exec jq \
	&& (addgroup -S docker 2>/dev/null || true) \
	&& addgroup -S -g 10001 runner \
	&& adduser -S -D -u 10001 -G runner -h /runner runner \
	&& addgroup runner docker \
	&& mkdir -p /runner/.gitslice /var/cache/gitslice-checks \
	&& chown -R runner:runner /runner /var/cache/gitslice-checks \
	&& chmod 0700 /runner /runner/.gitslice /var/cache/gitslice-checks

COPY gs /usr/local/bin/gs
COPY start.sh /usr/local/bin/start-runner
RUN chmod 0755 /usr/local/bin/gs /usr/local/bin/start-runner

WORKDIR /runner
ENTRYPOINT ["/usr/local/bin/start-runner"]
