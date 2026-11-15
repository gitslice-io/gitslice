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
agents=${AGENTS_URL:-https://agents.gitslice.io}
git_base=${GITSLICE_GIT:-https://gitslice.io/git}
: "${AGENTS_API_KEY:?set AGENTS_API_KEY}"

call() {
  curl -fsS -X POST -H "authorization: Bearer $AGENTS_API_KEY" -H 'content-type: application/json' -d "$1" "$agents/v1/slices/$slice/baseline"
}
field() { python3 -c "import json,sys; print(json.load(sys.stdin)['$1'])"; }

created=$(call '{"action":"create"}')
repo=$(field repo <<<"$created")
remote=$(field remote <<<"$created")
token=$(field token <<<"$created")

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
auth=()
if [ -n "${GITSLICE_TOKEN:-}" ]; then auth=(-c "http.extraHeader=Authorization: Bearer $GITSLICE_TOKEN"); fi
git "${auth[@]}" clone -q --bare "$git_base/$slice.git" "$work/slice.git"
git -C "$work/slice.git" -c "http.extraHeader=Authorization: Bearer $token" push -q "$remote" refs/heads/main:refs/heads/main

call "{\"action\":\"adopt\",\"repo\":\"$repo\"}" >/dev/null
echo "baseline $repo now serves $slice"
