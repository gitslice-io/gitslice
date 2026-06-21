#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WEB_ROOT="$ROOT/web"
ENV_FILE="$ROOT/.env.staging"

if [[ ! -f "$ENV_FILE" ]]; then
  echo "missing $ENV_FILE" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

export VITE_API_BASE_URL="${VITE_API_BASE_URL:-${PUBLIC_API_BASE_URL:-}}"
export VITE_GITSLICE_GIT_HTTP_BASE_URL="${VITE_GITSLICE_GIT_HTTP_BASE_URL:-${PUBLIC_GITSLICE_GIT_HTTP_BASE_URL:-}}"

if [[ -z "${VITE_CLERK_PUBLISHABLE_KEY:-}" ]]; then
  echo "VITE_CLERK_PUBLISHABLE_KEY is required in $ENV_FILE" >&2
  exit 1
fi
if [[ -z "${VITE_API_BASE_URL:-}" ]]; then
  echo "VITE_API_BASE_URL or PUBLIC_API_BASE_URL is required in $ENV_FILE" >&2
  exit 1
fi
if [[ -z "${VITE_GITSLICE_GIT_HTTP_BASE_URL:-}" ]]; then
  echo "VITE_GITSLICE_GIT_HTTP_BASE_URL or PUBLIC_GITSLICE_GIT_HTTP_BASE_URL is required in $ENV_FILE" >&2
  exit 1
fi

cd "$WEB_ROOT"
npm run build
if [[ -n "${CLERK_SECRET_KEY:-}" ]]; then
  printf '%s' "$CLERK_SECRET_KEY" | npx wrangler secret put CLERK_SECRET_KEY --env staging
fi
# Use npx so the locally-installed wrangler resolves regardless of how this
# script is invoked (bare `wrangler` only works when node_modules/.bin is on
# PATH, e.g. under `npm run`).
npx wrangler deploy --env staging
