# 21. Operations & Self-Hosting

Gitslice is self-hosted in the public slice `gitslice/gitslice` (canonical path
`/gitslice/gitslice`). Every change lands as a native changeset gated by
required checks, deployments and releases run directly from Gitslice via
[34_automations.md](34_automations.md), `go install` resolves through Gitslice's
own Git endpoint, and GitHub (`gitslice-io/gitslice`) is maintained strictly as
a read-only mirror.

## Canonical Source & Daily Loop

- **Organization & slice:** Organization `gitslice`, public slice
  `gitslice/gitslice` (path `/gitslice/gitslice`).
- **Workspace:** `gs init gitslice/gitslice` materializes the Go module under
  `gitslice/gitslice/`.
- **CI checks:** Defined in `.gitslice/checks.yaml` and `web/.gitslice/checks.yaml`
  and started by `.gitslice/automations.yaml` (`ci`) and
  `web/.gitslice/automations.yaml` on the account's CI runner pool
  ([17_continuous_integration.md](17_continuous_integration.md)).
  Required checks gate submit (`gitslice/gofmt`, `gitslice/vet`,
  `gitslice/test`, `gitslice/build`, `gitslice/e2e`, `gitslice/web/build`).
- **Public URLs:** All public names live under `gitslice.io` — source links
  (`https://gitslice.io/slices/gitslice/gitslice`), Go import path
  (`gitslice.io/gitslice`), install script (`https://gitslice.io/install.sh`),
  and release downloads (`https://gitslice.io/releases/...`).

## Direct Deploys From Gitslice

Production deployments no longer depend on GitHub or Workers Builds; they run
directly from `gitslice/gitslice` `main` via `.gitslice/automations.yaml`.

### Server (`gitslice-prod` on Cloud Run)

- **Automations:** `deploy-server`, `rollback-server`, and `ops-selftest` in
  `.gitslice/automations.yaml` (sharing `concurrency_group: prod-server` so
  deploys and rollbacks never overlap).
- **Flow:**
  1. A writer or admin triggers `gs auto run deploy-server --follow` (or from
     the folder's Automation tab).
  2. The worker `.gitslice/ops/runbook.js` posts the run and its callback URL
     to the Cloud Build webhook trigger **`gs-ops-webhook`**
     (`ops/automation/cloudbuild.yaml`, authenticated via the folder secret
     `OPS_WEBHOOK_QUERY` through the `cloudbuild.googleapis.com` network gate).
  3. `ops/automation/cloudbuild.yaml` clones `https://gitslice.io/git/gitslice/gitslice.git`
     at `main` and submits the repository's root `cloudbuild.yaml` (gate,
     build, migrate, deploy to `gitslice-prod` in `us-west1`), streaming
     progress and the final outcome back to the automation run's callback URL.
  4. `rollback-server` takes a validated Cloud Run revision name and routes
     100% traffic back to it without rebuilding.
- Updating `ops/automation/cloudbuild.yaml` requires regenerating and
  re-importing `ops/automation/trigger.yaml` via `gcloud builds triggers import`.

### Web App & Cloudflare Workers (`gitslice.io`, `triggers.gitslice.io`, `doorbell.gitslice.io`)

- **Automations:** `deploy-web` (`on: [push, manual]`, `paths: [web/**]`),
  `rollback-web`, `deploy-dispatcher`, `deploy-doorbell`, and
  `cloudflare-selftest` in `.gitslice/automations.yaml` (sharing
  `concurrency_group: prod-workers`).
- **Flow:**
  1. Runs as a `bash` automation (`runtime: node`, `checkout: folder`) on
     Gitslice's own `BashRunner` Cloudflare Container, executing
     `.gitslice/ops/cloudflare.sh`.
  2. `CLOUDFLARE_API_TOKEN` is injected sealed (`gs-secret:v1:`) and unwrapped
     by `Gate` only for `api.cloudflare.com`.
  3. Every landing touching `web/**` automatically builds and deploys the web
     Worker (`gitslice-web-production`) via `wrangler`; `rollback-web` shifts
     traffic to an earlier Worker version UUID.
  4. `deploy-dispatcher` updates `triggers.gitslice.io` (reusing its curated
     container images); `deploy-doorbell` updates `doorbell.gitslice.io`.

## Releases & R2 Hosting

CLI releases are built directly from Gitslice tags and served from Cloudflare
R2 — GitHub Actions (`release.yml`) and the legacy Homebrew tap are retired.

1. **Tag natively:** `gs tag create vX.Y.Z --slice gitslice/gitslice -m "..."`.
2. **Automated build (`release` / `publish-releases`):**
   - `tag.created` triggers the `release` automation in
     `.gitslice/automations.yaml` (sharing `concurrency_group: releases` with
     the manual `publish-releases` reconcile runbook).
   - `.gitslice/ops/runbook.js` invokes `gs-ops-webhook`
     (`ops/automation/cloudbuild.yaml`), which submits
     `ops/release/cloudbuild.yaml` (`ops/release/build.sh`) and follows the
     build to completion.
   - `build.sh` clones the tag from `https://gitslice.io/git/gitslice/gitslice.git`,
     builds the six platform archives plus `checksums.txt`, verifies the
     `linux/amd64` binary reports the tag, and uploads them to the production
     R2 bucket under `releases/<tag>/` along with `releases/latest.json`.
   - Cloud Scheduler job `gs-release` runs daily as a safety net for any missed
     tag event; `ops/release/release.sh run [<tag>]` supports manual rebuilds.
3. **Serving (`web/src/lib/releases.ts`):** The web Worker's `RELEASES` R2
   binding serves `/releases/latest` (redirect to `/releases/tag/<tag>`),
   `/releases/download/<tag>/<asset>`, and `/releases/latest/download/<asset>`.
   `install.sh` and `gs upgrade` download directly from `gitslice.io`;
   `install.sh` falls back to `go install gitslice.io/gitslice/cmd/gs@<version>`
   when no prebuilt binary matches or `GS_FROM_SOURCE=1`.

## Go Vanity Imports

The web Worker (`web/src/lib/goImport.ts`) serves `go-import` meta tags for
both Gitslice itself and any single-path slice hosted on Gitslice.

### `gitslice.io/gitslice`

`go install gitslice.io/gitslice/cmd/gs@latest` resolves via the fixed
`goModules` entry:

```html
<meta name="go-import" content="gitslice.io/gitslice git https://gitslice.io/git/gitslice/gitslice.git gitslice/gitslice">
```

- The fourth field (Go 1.25+ subdirectory support) points to the slice's
  subdirectory `gitslice/gitslice`.
- The Git projection automatically publishes subdirectory-prefixed semver tags
  (e.g. `gitslice/gitslice/v0.5.0`) for single-prefix slices.
- Users on `proxy.golang.org` work on any Go version; `GOPROXY=direct` users
  need Go 1.25+.

### Any Hosted Slice

For any other single-path slice `<account>/<slice>` (e.g. `acme/payment` with
included path `/acme/payment`), `sliceGoImportResponse` resolves the slice via
`ResolveSlice` and serves:

```html
<meta name="go-import" content="gitslice.io/acme/payment git https://gitslice.io/git/acme/payment.git acme/payment">
```

- The module's `go.mod` declares `module gitslice.io/acme/payment`; `gs tag
  create v1.0.0` publishes `acme/payment/v1.0.0`.
- Multi-path slices and whole-account slices return 404 with an explanation.
- Private slices work via `~/.netrc` (Basic auth password = Gitslice API token,
  forwarded as Bearer auth) with `GOPRIVATE=gitslice.io/<account>/*`.
- The Git smart HTTP endpoint accepts repository paths with or without `.git`
  so direct `gitslice.io/git/<account>/<slice>.git/<path>` imports also work.

## GitHub Read-Only Mirror

GitHub (`gitslice-io/gitslice`) is a one-way, read-only mirror of
`gitslice/gitslice`. Do not push directly to GitHub or open PRs there.

- **Exporter (`ops/mirror/`):** Reads the projected Git history of
  `gitslice/gitslice`, locates the sync point via the `Git-Commit` trailer,
  replays each new commit's slice subdirectory tree onto GitHub `main` with
  original authorship/messages, and pushes new tags. Refuses to run if `main`
  contains non-Gitslice commits or tree drift.
- **Workflows (`.github/workflows/mirror-export.yml`, `mirror-import.yml`):**
  Share one concurrency group and enforce the one-writable-side invariant via
  repository variables (`GITSLICE_EXPORT_ENABLED=true`,
  `GITSLICE_IMPORT_ENABLED=false`) to prevent echo loops.

## Break-Glass & Recovery

The deploy path must remain operable even if Gitslice production is down:

- **Server hotfix:** `cloudbuild.yaml` can be submitted directly from a local
  checkout (`gcloud builds submit --config cloudbuild.yaml ...`) without
  calling Gitslice or `gs-ops-webhook`.
- **Web / Worker hotfix:** `wrangler deploy` can be run locally from `web/`,
  `trigger-dispatch/`, or `doorbell/` with a local `.env.prod` (generated via
  `gs secret env gitslice/gitslice/env.prod.template -o .env.prod` when
  Gitslice is up, or from operator credentials during an outage).
- **Reconciliation:** After any break-glass deploy, land the same change as a
  normal Gitslice changeset so `gitslice/gitslice` remains the source of truth
  and the GitHub mirror stays in sync.
