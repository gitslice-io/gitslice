#!/usr/bin/env bash
# Installs the web app's dependencies for a check (web/.gitslice/checks.yaml).
#
# npm ci takes most of a minute on the CI host, and the dependencies change only
# when package-lock.json (or Node) does. So one install is kept per lock file and
# Node version, and linked into each check's fresh tree with hard links: reusing
# it copies nothing and takes no disk. Without a kept install, or when linking
# fails, this is a plain npm ci, whose result is kept for next time.
set -euo pipefail
cd "$(dirname "$0")/.."

key="$(cat package-lock.json <(node --version) | sha256sum | cut -c1-16)"
cache_root="${GS_CI_NODE_MODULES_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/gitslice-ci/node_modules}"
kept="$cache_root/$key"

if [ -d "$kept" ] && rm -rf node_modules && cp -al "$kept" node_modules 2>/dev/null; then
  echo "reused the install for package-lock $key"
  exit 0
fi

rm -rf node_modules
npm ci

# Keep this install. Link it in place first, then rename, so a check that
# starts meanwhile never sees half of one.
mkdir -p "$cache_root"
tmp="$(mktemp -d "$cache_root/.tmp-XXXXXX")"
if cp -al node_modules "$tmp/node_modules" 2>/dev/null && mv -T "$tmp/node_modules" "$kept" 2>/dev/null; then
  echo "kept the install for package-lock $key"
fi
rm -rf "$tmp"
# Keep the three newest installs.
ls -1t "$cache_root" | tail -n +4 | while read -r old; do rm -rf "${cache_root:?}/$old"; done
