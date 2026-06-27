---
name: deployment
description: Deploy and verify Gitslice staging/backend/web surfaces. Use when an agent is asked to deploy, release, restart staging, update the Cloudflare Worker web app, roll out backend binaries, prepare deployment checks, inspect deployment environment requirements, verify agenttools.dev or api.agenttools.dev, or troubleshoot PM2, Wrangler, CORS, R2, auth, migration, or deployed-service issues.
---

# Gitslice Deployment

## Core Rules

Treat deployment as live infrastructure work. Deploy only when the user explicitly asks for a deploy/restart/release, or when the user asks to fix a deployed service and deployment is the needed repair.

If the target is ambiguous, ask for the target. In this project, unqualified staging usually means:

- web: `https://agenttools.dev`
- API: `https://api.agenttools.dev`
- Cloudflare Worker env: `staging` in `web/wrangler.jsonc`
- backend process: PM2 process `gitslice-rewrite-staging`

Do not deploy production unless the user explicitly names production. Production hostnames exist in config, but this skill's concrete workflow is for staging.

Never print secret values. `.env.staging`, `.env.local`, `.env.production`, and `deploy/` are gitignored local operator state. To inspect environment shape, list keys only:

```bash
awk -F= '/^[A-Za-z_][A-Za-z0-9_]*=/ {print $1}' .env.staging | sort
```

Record material deploys and surprising deployment findings in `design/10_execution_log.md`: request, actions, decisions, verification commands, and results. Do not record secrets.

## Current Topology

Backend staging is a Go `gitslice-server` process behind nginx. `ops/nginx.conf` routes `api.agenttools.dev` gRPC services to `127.0.0.1:50052` and `/v1/` plus `/git/` HTTP traffic to `127.0.0.1:8081`.

The optional local helper `deploy/run-staging.sh` sources `.env.staging`, defaults `GITSLICE_HTTP_ADDR` to `127.0.0.1:8081`, and execs `bin/gitslice-server`. Treat it as operator-local context because `deploy/` is gitignored.

The web app deploys to Cloudflare Workers through `web/scripts/deploy-staging.sh`, normally via:

```bash
npm --prefix web run deploy:staging
```

That script sources root `.env.staging`, maps `PUBLIC_API_BASE_URL` into `VITE_API_BASE_URL`, maps `PUBLIC_GITSLICE_GIT_HTTP_BASE_URL` into `VITE_GITSLICE_GIT_HTTP_BASE_URL`, builds the Vite bundle, optionally updates the `CLERK_SECRET_KEY` Worker secret, then runs `wrangler deploy --env staging`.

## Preflight

Start with local state and scope:

```bash
git status --short
git diff --check
```

Use the default local gate before backend deploys unless the user has constrained time or scope:

```bash
go test ./...
go build ./cmd/...
```

For web-facing changes, run:

```bash
npm --prefix web run build
npm --prefix web test
```

For changes touching submit, conflict detection, concurrency, storage, migrations, or Git projection, prefer the real PostgreSQL gate:

```bash
make functional
```

Run the load gate only for contention or performance-sensitive paths:

```bash
make load
```

If a gate is skipped, say exactly which one and why before or after deployment.

## Backend Staging

Build fresh binaries from the repository root:

```bash
go build -o bin/gitslice-server ./cmd/gitslice-server
go build -o bin/gs ./cmd/gs
```

Restart the existing PM2 staging process:

```bash
npx --yes pm2 restart gitslice-rewrite-staging --update-env
npx --yes pm2 show gitslice-rewrite-staging
```

If agent daemon behavior changed, also rebuild the staging agent CLI before restarting or checking the daemon:

```bash
go build -o /home/nic/.local/bin/gs-staging-agent ./cmd/gs
/home/nic/.local/bin/gs-staging-agent agent status --json
```

Server startup runs PostgreSQL migrations by default. `GITSLICE_RUN_MIGRATIONS=0` disables that behavior; do not disable migrations to hide a failing deploy unless the user explicitly requests a rollback or emergency mitigation.

For R2-backed staging, required object-store settings come from `OBJECT_STORE_TYPE=r2` and `R2_ENDPOINT`, `R2_BUCKET`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, with optional `R2_REGION`, `R2_PREFIX`, and `R2_USE_PATH_STYLE`.

For Clerk-backed auth, `AUTH_PROVIDER=clerk`, `CLERK_SECRET_KEY`, and `CLERK_PUBLISHABLE_KEY` are expected. Service-token verification is optional and depends on `GITSLICE_SERVICE_JWT_*` settings.

## Web Staging

Deploy the Cloudflare Worker from the repository root:

```bash
npm --prefix web run deploy:staging
```

The deploy script requires:

- `VITE_CLERK_PUBLISHABLE_KEY`
- `VITE_API_BASE_URL` or `PUBLIC_API_BASE_URL`
- `VITE_GITSLICE_GIT_HTTP_BASE_URL` or `PUBLIC_GITSLICE_GIT_HTTP_BASE_URL`

If the deployed web bundle points at localhost or an old API host, rebuild and redeploy through `web/scripts/deploy-staging.sh`; Wrangler runtime vars are not visible to `import.meta.env` during the Vite build.

## Verification

Verify the API after backend or full-stack deploys:

```bash
curl -sS -i --max-time 20 -X POST https://api.agenttools.dev/gitslice.core.v1.AuthService/GetAuthStatus -H 'Content-Type: application/json' --data '{}'
curl -sS -i --max-time 20 -X OPTIONS https://api.agenttools.dev/gitslice.core.v1.AuthService/GetAuthStatus -H 'Origin: https://agenttools.dev' -H 'Access-Control-Request-Method: POST' -H 'Access-Control-Request-Headers: authorization,content-type'
```

Verify the web app after web or full-stack deploys:

```bash
curl -sS --max-time 20 -H 'Accept-Encoding: identity' https://agenttools.dev/ | rg -o '/assets/index-[^" ]+\.js'
curl -sS -i --max-time 20 https://agenttools.dev/
```

For page-specific work, hit the exact route involved, then fetch the current asset and search for the new route, API host, RPC service name, or UI string that should be present.

For PM2 state:

```bash
npx --yes pm2 list
npx --yes pm2 show gitslice-rewrite-staging
```

## Troubleshooting

If browser calls fail with CORS errors, check `GITSLICE_HTTP_ALLOWED_ORIGIN`, nginx routing for `api.agenttools.dev`, and the OPTIONS verification command above.

If API requests fail with content-type or gRPC protocol errors, check whether nginx is sending gRPC service paths to the gRPC listener and JSON `/v1/` requests to the HTTP gateway.

If object writes or reads fail only after deployment, inspect R2 configuration keys and the backend logs. The filesystem object store is prototype-only and should not be assumed for production-style staging.

If migrations fail on restart, the server should not come up cleanly. Fix or roll back the migration rather than mutating schema state manually.

If `wrangler` cannot deploy, confirm the local npm dependency is installed and `CLOUDFLARE_API_TOKEN` is available in the operator environment. Prefer `npm --prefix web run deploy:staging` so the local `wrangler` binary is used.
