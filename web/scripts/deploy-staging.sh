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
wrangler deploy --env staging
