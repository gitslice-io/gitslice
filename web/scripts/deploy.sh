#!/usr/bin/env bash
# Build and deploy the Cloudflare Worker web app for a target environment.
#
# Usage: deploy.sh <staging|production>
#   staging    -> sources ../.env.staging, wrangler --env staging  (agenttools.dev)
#   production -> sources ../.env.prod,    wrangler --env production (gitslice.io)
#
# Single source of truth for the web deploy: the only per-env differences are the
# env file and the wrangler env name, resolved below. Invoked via the
# `deploy:staging` / `deploy:production` npm scripts.
set -euo pipefail

ENV="${1:-}"
case "$ENV" in
  staging)    ENV_FILE_NAME=".env.staging" ;;
  production) ENV_FILE_NAME=".env.prod" ;;   # note: prod file is .env.prod
  *) echo "usage: $0 <staging|production>" >&2; exit 2 ;;
esac

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WEB_ROOT="$ROOT/web"
ENV_FILE="$ROOT/$ENV_FILE_NAME"

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
# Tag analytics events with the deploy target (staging|production) unless the
# env file already pins VITE_POSTHOG_ENV explicitly.
export VITE_POSTHOG_ENV="${VITE_POSTHOG_ENV:-$ENV}"

if [[ -z "${VITE_CLERK_PUBLISHABLE_KEY:-}" ]]; then
  echo "VITE_CLERK_PUBLISHABLE_KEY is required in $ENV_FILE" >&2
  exit 1
fi
if [[ -z "${VITE_CLERK_AUTHORIZED_PARTIES:-}" ]]; then
  echo "VITE_CLERK_AUTHORIZED_PARTIES is required in $ENV_FILE" >&2
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
  printf '%s' "$CLERK_SECRET_KEY" | npx wrangler secret put CLERK_SECRET_KEY --env "$ENV"
fi
# Use npx so the locally-installed wrangler resolves regardless of how this
# script is invoked (bare `wrangler` only works when node_modules/.bin is on
# PATH, e.g. under `npm run`).
npx wrangler deploy --env "$ENV"
