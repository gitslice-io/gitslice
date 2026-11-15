#!/usr/bin/env bash
# Publish a slice's Git projection to a new Artifacts baseline by pushing it,
# then make it the baseline new agent sessions fork. Use it for slices that
# Artifacts cannot import on its own, such as private slices.
#
#   AGENTS_API_KEY=... scripts/publish-baseline.sh demo/storefront
#
# Optional: AGENTS_URL (default https://agents.gitslice.io), GITSLICE_GIT
# (default https://gitslice.io/git), GITSLICE_TOKEN (to clone a private slice).
set -euo pipefail

slice=${1:?usage: publish-baseline.sh account/slice}
if [[ ! "$slice" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || [[ "$slice" == *".."* ]]; then
  echo "invalid slice name: $slice" >&2
  exit 1
fi

agents=${AGENTS_URL:-https://agents.gitslice.io}
git_base=${GITSLICE_GIT:-https://gitslice.io/git}
: "${AGENTS_API_KEY:?set AGENTS_API_KEY}"

reject_control() {
  case "$1" in
    *$'\r'*|*$'\n'*|*$'"'*|*'\'*)
      echo "refusing secret containing control, quote, or backslash characters" >&2
      exit 1
      ;;
  esac
}
reject_control "$AGENTS_API_KEY"

call() {
  local payload=$1
  printf 'header = "authorization: Bearer %s"\n' "$AGENTS_API_KEY" |
    curl -fsS --config - \
      -X POST \
      -H 'content-type: application/json' \
      --data-binary "$payload" \
      "${agents%/}/v1/slices/$slice/baseline"
}

field() {
  python3 -c 'import json, sys; print(json.load(sys.stdin)[sys.argv[1]])' "$1"
}

created=$(call '{"action":"create"}')
repo=$(field repo <<<"$created")
remote=$(field remote <<<"$created")
token=$(field token <<<"$created")

reject_control "$token"
if [[ "$remote" != https://* ]]; then
  echo "refusing non-https Artifacts remote: $remote" >&2
  exit 1
fi

work=$(mktemp -d)
chmod 700 "$work"
trap 'rm -rf "$work"' EXIT

export GIT_TERMINAL_PROMPT=0
export GIT_ALLOW_PROTOCOL=https

clone_url="${git_base%/}/$slice.git"
if [ -n "${GITSLICE_TOKEN:-}" ]; then
  reject_control "$GITSLICE_TOKEN"
  GIT_CONFIG_COUNT=2 \
  GIT_CONFIG_KEY_0=http.followRedirects \
  GIT_CONFIG_VALUE_0=false \
  GIT_CONFIG_KEY_1="http.${clone_url}.extraHeader" \
  GIT_CONFIG_VALUE_1="Authorization: Bearer $GITSLICE_TOKEN" \
    git clone -q --bare -- "$clone_url" "$work/slice.git"
else
  GIT_CONFIG_COUNT=1 \
  GIT_CONFIG_KEY_0=http.followRedirects \
  GIT_CONFIG_VALUE_0=false \
    git clone -q --bare -- "$clone_url" "$work/slice.git"
fi

GIT_CONFIG_COUNT=2 \
GIT_CONFIG_KEY_0=http.followRedirects \
GIT_CONFIG_VALUE_0=false \
GIT_CONFIG_KEY_1="http.${remote}.extraHeader" \
GIT_CONFIG_VALUE_1="Authorization: Bearer $token" \
  git -C "$work/slice.git" push -q -- "$remote" refs/heads/main:refs/heads/main

adopt_payload=$(python3 -c 'import json, sys; print(json.dumps({"action": "adopt", "repo": sys.argv[1]}))' "$repo")
call "$adopt_payload" >/dev/null
echo "baseline $repo now serves $slice"
