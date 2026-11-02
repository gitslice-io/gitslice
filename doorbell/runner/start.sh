#!/bin/sh
# Starts Docker without iptables (a Cloudflare container has none), then runs
# a checks-only daemon that exits once idle. The Worker passes
# GITSLICE_SERVER_ADDR, GITSLICE_RUNNER_TOKEN, GITSLICE_RUNNER_SUBJECT and
# GITSLICE_RUNNER_NAME.
set -eu

dockerd-entrypoint.sh dockerd --iptables=false --ip6tables=false --ip-forward=false \
	--exec-opt native.cgroupdriver=cgroupfs >/var/log/dockerd.log 2>&1 &
if ! timeout 60 sh -c 'until docker version >/dev/null 2>&1; do sleep 0.2; done'; then
	echo "docker did not start:" >&2
	tail -50 /var/log/dockerd.log >&2
	exit 1
fi

export HOME=/runner
mkdir -p "$HOME/.gitslice"
umask 077
printf '{"server_addr":"%s","token":"%s","subject_id":"%s"}\n' \
	"$GITSLICE_SERVER_ADDR" "$GITSLICE_RUNNER_TOKEN" "$GITSLICE_RUNNER_SUBJECT" >"$HOME/.gitslice/config.json"
unset GITSLICE_RUNNER_TOKEN

# Bridge networks cannot reach the internet without iptables: builds,
# networked checks and services use the host's network.
export GITSLICE_CHECK_HOST_NETWORK=1
export GITSLICE_CHECK_CONCURRENCY="${GITSLICE_CHECK_CONCURRENCY:-1}"
exec gs agent start --ci --name "${GITSLICE_RUNNER_NAME:-cf-ci}" --detach-after 0 \
	--idle-timeout "${GITSLICE_RUNNER_IDLE:-90s}"
