#!/bin/sh
# Starts Docker without iptables (a Cloudflare container has none), then runs
# a checks-only daemon as an unprivileged user with host-mode checks disabled.
# The Worker passes GITSLICE_SERVER_ADDR, GITSLICE_RUNNER_TOKEN,
# GITSLICE_RUNNER_SUBJECT and GITSLICE_RUNNER_NAME.
set -eu

json_escape() {
	awk 'BEGIN {
		s = ARGV[1]
		ARGV[1] = ""
		out = ""
		for (i = 1; i <= length(s); i++) {
			c = substr(s, i, 1)
			if (c == "\\") out = out "\\\\"
			else if (c == "\"") out = out "\\\""
			else if (c == "\b") out = out "\\b"
			else if (c == "\f") out = out "\\f"
			else if (c == "\n") out = out "\\n"
			else if (c == "\r") out = out "\\r"
			else if (c == "\t") out = out "\\t"
			else out = out c
		}
		printf "%s", out
	}' "$1"
}

write_runner_config() {
	dest="$1"
	umask 077
	mkdir -p "$(dirname "$dest")"
	chmod 0700 "$(dirname "$dest")"
	if command -v jq >/dev/null 2>&1; then
		jq -nc \
			--arg server_addr "${GITSLICE_SERVER_ADDR:-}" \
			--arg token "${GITSLICE_RUNNER_TOKEN:-}" \
			--arg subject_id "${GITSLICE_RUNNER_SUBJECT:-}" \
			'{server_addr: $server_addr, token: $token, subject_id: $subject_id}' >"$dest"
	else
		printf '{"server_addr":"%s","token":"%s","subject_id":"%s"}\n' \
			"$(json_escape "${GITSLICE_SERVER_ADDR:-}")" \
			"$(json_escape "${GITSLICE_RUNNER_TOKEN:-}")" \
			"$(json_escape "${GITSLICE_RUNNER_SUBJECT:-}")" >"$dest"
	fi
	chmod 0600 "$dest"
}

if [ "${1:-}" = "--write-config-only" ]; then
	write_runner_config "${2:?missing config path}"
	exit 0
fi

dockerd-entrypoint.sh dockerd --group docker --iptables=false --ip6tables=false --ip-forward=false \
	--exec-opt native.cgroupdriver=cgroupfs >/var/log/dockerd.log 2>&1 &
if ! timeout 60 sh -c 'until docker version >/dev/null 2>&1; do sleep 0.2; done'; then
	echo "docker did not start:" >&2
	tail -50 /var/log/dockerd.log >&2
	exit 1
fi
chgrp docker /var/run/docker.sock 2>/dev/null || true
chmod 0660 /var/run/docker.sock 2>/dev/null || true

export HOME=/runner
mkdir -p "$HOME/.gitslice" /var/cache/gitslice-checks
chmod 0700 "$HOME" "$HOME/.gitslice" /var/cache/gitslice-checks
write_runner_config "$HOME/.gitslice/config.json"
unset GITSLICE_RUNNER_TOKEN

# Never allow untrusted check commands to run directly on the runner VM host:
# every check must run inside a container (which does not mount /runner).
export GITSLICE_CHECKS_REQUIRE_CONTAINER=1
export GITSLICE_CHECK_CACHE_DIR=/var/cache/gitslice-checks
# Bridge networks cannot reach the internet without iptables: builds,
# networked checks and services use the host's network.
export GITSLICE_CHECK_HOST_NETWORK=1
export GITSLICE_CHECK_CONCURRENCY="${GITSLICE_CHECK_CONCURRENCY:-1}"

if id -u runner >/dev/null 2>&1 && command -v su-exec >/dev/null 2>&1; then
	chown -R runner:runner "$HOME" /var/cache/gitslice-checks
	chmod 0700 "$HOME" "$HOME/.gitslice" /var/cache/gitslice-checks
	chmod 0600 "$HOME/.gitslice/config.json"
	exec su-exec runner gs agent start --ci --name "${GITSLICE_RUNNER_NAME:-cf-ci}" --detach-after 0 \
		--idle-timeout "${GITSLICE_RUNNER_IDLE:-90s}"
fi

echo "refusing to start runner without unprivileged runner user and su-exec" >&2
exit 1
