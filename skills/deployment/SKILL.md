---
name: deployment
description: Deploy and verify Gitslice staging/production backend/web surfaces. Use when an agent is asked to deploy, release, restart staging, update the Cloudflare Worker web app, roll out backend binaries, prepare deployment checks, inspect deployment environment requirements, verify agenttools.dev / api.agenttools.dev (staging) or gitslice.io / api.gitslice.io (production), or troubleshoot PM2, Wrangler, Cloud Run, Cloud Build, CORS, R2, Neon, auth, migration, or deployed-service issues.
---

# Gitslice Deployment

## Core Rules

Treat deployment as live infrastructure work. Deploy only when the user explicitly asks for a deploy/restart/release, or when the user asks to fix a deployed service and deployment is the needed repair.

If the target is ambiguous, ask for the target. Two environments exist:

**Staging** (unqualified "staging"):
- web: `https://agenttools.dev` — Cloudflare Worker env `staging` in `web/wrangler.jsonc`
- API: `https://api.agenttools.dev` — Go `gitslice-server` behind nginx, PM2 process `gitslice-rewrite-staging`

**Production** (only when the user explicitly names production):
- web: `https://gitslice.io` — Cloudflare Worker env `production` (`gitslice-web-production`)
- API: `https://api.gitslice.io` — Cloud Run service `gitslice-prod` (region `us-west1`), Neon Postgres, R2 object store
- deploy path: Cloud Build pipeline `cloudbuild.yaml`, run **daily** by Cloud Scheduler (not on merge)

Do not deploy production unless the user explicitly names production. Note the production **API** ships on a **daily schedule**: Cloud Scheduler runs the Cloud Build trigger once a day, and the pipeline no-ops when `main` hasn't moved since the last deploy. Merging to `main` does **not** deploy immediately — merged backend code ships with the next scheduled build unless the user asks to ship it now (then run the trigger manually) — see "Production (Cloud Run + gitslice.io)" and "Production Deploy Monitoring & Rollback" below. Only the web Worker is deployed manually.

Never print secret values. `.env.staging`, `.env.prod`, `.env.local`, and `deploy/` are gitignored local operator state. To inspect environment shape, list keys only:

```bash
awk -F= '/^[A-Za-z_][A-Za-z0-9_]*=/ {print $1}' .env.staging | sort
```

Record material deploys and surprising deployment findings in `design/10_execution_log.md`: request, actions, decisions, verification commands, and results. Do not record secrets.

## Current Topology

Backend staging is a Go `gitslice-server` process behind nginx. `ops/nginx.conf` routes `api.agenttools.dev` gRPC services to `127.0.0.1:50052` and `/v1/` plus `/git/` HTTP traffic to `127.0.0.1:8081`.

The optional local helper `deploy/run-staging.sh` sources `.env.staging`, defaults `GITSLICE_HTTP_ADDR` to `127.0.0.1:8081`, and execs `bin/gitslice-server`. Treat it as operator-local context because `deploy/` is gitignored.

The web app deploys to Cloudflare Workers through a single script `web/scripts/deploy.sh <staging|production>`, normally via the npm wrappers:

```bash
npm --prefix web run deploy:staging      # -> deploy.sh staging   (agenttools.dev)
npm --prefix web run deploy:production   # -> deploy.sh production (gitslice.io)
```

That script picks the env file per target (`.env.staging` / `.env.prod`), maps `PUBLIC_API_BASE_URL` into `VITE_API_BASE_URL`, maps `PUBLIC_GITSLICE_GIT_HTTP_BASE_URL` into `VITE_GITSLICE_GIT_HTTP_BASE_URL`, builds the bundle (TanStack Start SSR → `.output/`), updates the `CLERK_SECRET_KEY` Worker secret when present (required for SSR — see the Web section), then runs `wrangler deploy --env <target>`. Worker envs and their custom-domain routes are defined under `env` in `web/wrangler.jsonc`.

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

For Clerk-backed auth, `AUTH_PROVIDER=clerk`, `CLERK_SECRET_KEY`,
`CLERK_PUBLISHABLE_KEY`, `CLERK_ISSUER`, and
`CLERK_AUTHORIZED_PARTIES` are expected. Service-token verification is optional
and depends on `GITSLICE_SERVICE_JWT_*` settings.

## Web (Staging & Production)

Deploy the Cloudflare Worker from the repository root:

```bash
npm --prefix web run deploy:staging      # agenttools.dev
npm --prefix web run deploy:production   # gitslice.io
```

The deploy script (`web/scripts/deploy.sh <env>`) requires, in the matching env file:

- `VITE_CLERK_PUBLISHABLE_KEY`
- `VITE_CLERK_AUTHORIZED_PARTIES`
- `VITE_API_BASE_URL` or `PUBLIC_API_BASE_URL`
- `VITE_GITSLICE_GIT_HTTP_BASE_URL` or `PUBLIC_GITSLICE_GIT_HTTP_BASE_URL`
- `CLERK_SECRET_KEY` — **required for SSR, not optional.** `web/src/start.ts` runs Clerk's `clerkMiddleware` on every server render; without this Worker secret the SSR throws and the site returns 500 `{"message":"HTTPError"}`. The deploy script `wrangler secret put`s it when present in the env file. (Distinct from the Go backend, which uses only the publishable key — the *web Worker* additionally needs the secret key from the same Clerk instance.)

`CLOUDFLARE_API_TOKEN` must be in the env file (or operator env) with Workers Scripts:Edit and, for custom-domain routes, Workers Routes:Edit. If the deployed web bundle points at localhost or an old API host, rebuild and redeploy through `web/scripts/deploy.sh`; Wrangler runtime vars are not visible to `import.meta.env` during the build.

## Production (Cloud Run + gitslice.io)

The API backend is a container on Cloud Run (service `gitslice-prod`, region `us-west1`), not the PM2 process; the DB is Neon and object storage is R2.

**The production API deploys on a daily schedule: Cloud Scheduler job `gitslice-prod-daily-build` (us-west1) runs the Cloud Build trigger against `main` once a day, and the pipeline's `gate` step skips the whole build when the deployed image tag already matches HEAD. Do not manually deploy Cloud Run to ship merged code.** Merges to `main` no longer trigger builds (the trigger's GitHub push event is disabled); merged code waits for the next scheduled run. To ship merged code immediately when the user asks, run the trigger by hand — `gcloud builds triggers run <TRIGGER_ID> --branch=main` — then monitor it (see "Production Deploy Monitoring & Rollback"). Avoid manual `gcloud run deploy` / `gcloud run services update` for routine releases: the next pipeline run resets flags to `cloudbuild.yaml`'s values, so ad-hoc flag changes silently revert — change `cloudbuild.yaml` (or the trigger substitutions) instead.

The pipeline is `cloudbuild.yaml` at the repo root: `gate (skip when HEAD is already deployed) → config → build (Dockerfile) → push → migrate (one-shot Cloud Run Job, `-migrate-only`) → deploy`. The Cloud Build trigger (`filename: cloudbuild.yaml`) supplies substitutions `_DEPLOY_REGION`, `_SERVICE_NAME`, `_AR_HOSTNAME`, `_AR_REPOSITORY`, `_R2_ENDPOINT`, `_R2_BUCKET`, `_CLERK_PUBLISHABLE_KEY`, `_CLERK_ISSUER`, `_ALLOWED_ORIGIN`, `_TAG=$SHORT_SHA`; unset substitutions (e.g. `_MIN_INSTANCES`, `_PUBLISH_INTERVAL_MS`) fall back to the defaults in `cloudbuild.yaml`. Manual pipeline runs are only for exceptional cases (e.g. re-deploying an unchanged tree after an infra fix): `gcloud builds submit --config cloudbuild.yaml --substitutions=...` or the gitignored local wrapper `deploy/cloudrun.sh`.

Key production facts:
- Serving instances run with `GITSLICE_RUN_MIGRATIONS=0`; migrations run in the pipeline's migrate Job, so schema changes ship on the next deploy.
- Runs single-port h2c with `--use-http2`; background workers require `--no-cpu-throttling` (set by the pipeline). `--min-instances=0`: the service scales to zero when idle — safe since the outbox/publisher adaptive backoff (#299); background workers pause with the instance and Cloud Run's scale decisions ignore them.
- Secrets live in Secret Manager: `gitslice-database-url`, `gitslice-r2-access-key-id`, `gitslice-r2-secret-access-key`, `gitslice-metrics-token` (runtime SA needs `secretmanager.secretAccessor`). The Clerk publishable key is a non-secret env var; the server does not use a Clerk secret key.
- CORS: `GITSLICE_HTTP_ALLOWED_ORIGIN=https://gitslice.io` (Cloud Build substitution `_ALLOWED_ORIGIN`).
- `api.gitslice.io` is a Cloud Run domain mapping (CNAME → `ghs.googlehosted.com`, DNS-only); its Google-managed cert can take up to ~1 hour to provision after DNS is set.

The web app then deploys with `npm --prefix web run deploy:production` (custom domain `gitslice.io`), pointing `PUBLIC_API_BASE_URL` at `https://api.gitslice.io`. The web Worker does **not** auto-deploy on merge — it remains a manual step.

## Production Deploy Monitoring & Rollback

The pipeline runs on the daily schedule (or when run manually via `gcloud builds triggers run`). Monitor a run with:

```bash
gcloud builds list --limit=3 --format="table(id,createTime,status,substitutions.SHORT_SHA)"
gcloud builds log <BUILD_ID> --stream        # follow a WORKING build; builds take several minutes
```

When the build reports SUCCESS, confirm the new revision is serving and healthy:

```bash
gcloud run revisions list --service=gitslice-prod --region=us-west1 --limit=3 \
  --format="table(metadata.name,metadata.creationTimestamp,status.conditions[0].status)"
gcloud run services describe gitslice-prod --region=us-west1 --format="value(status.traffic)"
```

Then run the production API curl checks in "Verification" below. For changes with visible behavior, verify that behavior specifically (e.g. hit the affected RPC), not just liveness.

If the build FAILs:
- `gcloud builds log <BUILD_ID>` and identify the failing step (`gate`/`config`/`build`/`push`/`migrate`/`deploy`).
- A `migrate` failure aborts before `deploy` — the previous revision keeps serving; fix the migration, merge the fix, and run the trigger manually (or wait for the next scheduled run).
- A `deploy` failure or an unhealthy new revision: check `gcloud run revisions list` conditions and service logs (`gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="gitslice-prod" AND severity>=ERROR' --freshness=30m --limit=50`).

If a bad revision is serving, roll back by pinning traffic to the last good revision:

```bash
gcloud run services update-traffic gitslice-prod --region=us-west1 --to-revisions=<GOOD_REVISION>=100
```

This pins traffic; later, after a fixed build deploys, restore following latest:

```bash
gcloud run services update-traffic gitslice-prod --region=us-west1 --to-latest
```

Record failed builds and rollbacks in `design/10_execution_log.md`.

## Verification

Verify the API after backend or full-stack deploys:

```bash
curl -sS -i --max-time 20 -X POST https://api.agenttools.dev/gitslice.core.v1.AuthService/GetAuthStatus -H 'Content-Type: application/json' --data '{}'
curl -sS -i --max-time 20 -X OPTIONS https://api.agenttools.dev/gitslice.core.v1.AuthService/GetAuthStatus -H 'Origin: https://agenttools.dev' -H 'Access-Control-Request-Method: POST' -H 'Access-Control-Request-Headers: authorization,content-type'
```

For production, use the production hosts:

```bash
curl -sS -i --max-time 20 -X POST https://api.gitslice.io/gitslice.core.v1.AuthService/StartCliLogin -H 'Content-Type: application/json' --data '{}'
curl -sS -i --max-time 20 -X OPTIONS https://api.gitslice.io/gitslice.core.v1.AuthService/GetAuthStatus -H 'Origin: https://gitslice.io' -H 'Access-Control-Request-Method: POST' -H 'Access-Control-Request-Headers: authorization,content-type'
```

`StartCliLogin` is a public (no-auth) RPC that returns 200 — good for confirming the API is up. `GetAuthStatus` requires auth and returns 401 when unauthenticated (still proves the server is serving). A `000`/connection failure on `api.gitslice.io` usually means the Cloud Run domain-mapping cert is still provisioning.

Verify the web app after web or full-stack deploys:

```bash
curl -sS --max-time 20 -H 'Accept-Encoding: identity' https://agenttools.dev/ | rg -o '/assets/index-[^" ]+\.js'   # staging
curl -sS -i --max-time 20 https://gitslice.io/                                                                     # production
```

Note: the production web app SSR fetches `api.gitslice.io` during render, so `gitslice.io` returns 500 (`{"message":"HTTPError"}`) until the API domain's cert is live.

For page-specific work, hit the exact route involved, then fetch the current asset and search for the new route, API host, RPC service name, or UI string that should be present.

For PM2 state:

```bash
npx --yes pm2 list
npx --yes pm2 show gitslice-rewrite-staging
```

## Troubleshooting

If the web app itself returns 500 `{"message":"HTTPError"}` (even when the API is up), the Worker is missing the `CLERK_SECRET_KEY` secret — the SSR `clerkMiddleware` needs it. Fix with `printf '%s' "$CLERK_SECRET_KEY" | npx wrangler secret put CLERK_SECRET_KEY --env <staging|production>` (run from `web/`), and add the key to the env file so future deploys set it.

If browser calls fail with CORS errors, check `GITSLICE_HTTP_ALLOWED_ORIGIN` (staging: `https://agenttools.dev` via nginx/PM2 env; production: `https://gitslice.io` via the `_ALLOWED_ORIGIN` Cloud Build substitution — a mismatch here breaks the web app's calls), and the OPTIONS verification command above.

If API requests fail with content-type or gRPC protocol errors, check whether nginx is sending gRPC service paths to the gRPC listener and JSON `/v1/` requests to the HTTP gateway.

If object writes or reads fail only after deployment, inspect R2 configuration keys and the backend logs. The filesystem object store is prototype-only and should not be assumed for production-style staging.

If migrations fail on restart, the server should not come up cleanly. Fix or roll back the migration rather than mutating schema state manually.

If `wrangler` cannot deploy, confirm the local npm dependency is installed and `CLOUDFLARE_API_TOKEN` is available in the env file. Prefer the npm wrappers (`deploy:staging` / `deploy:production`) so the local `wrangler` binary is used. If the script deploys but the custom-domain route fails, the token is likely missing Workers Routes / DNS permissions on the zone.

If a production Cloud Build fails, pull the log with `gcloud builds log <id>`; a container that exits on `GITSLICE_DATABASE_URL is required` (or similar) means the deploy bypassed `cloudbuild.yaml` (e.g. a `gcloud run deploy --source` / wizard inline build) and never set env/secrets — deploy through the pipeline instead.
