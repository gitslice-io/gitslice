# 18. Product Analytics (PostHog)

## Goal

Add a **unified product-analytics event pipeline** across the web app and the
Go server so we can answer product questions (who signs up, which slices get
created, how far changesets get through submit/merge, where users drop off)
against **one identity per user**. This is product/behavioral analytics — it is
**not** engineering observability. The existing Prometheus layer
(`internal/metrics`, `server/observability.go`) stays as-is and owns
traces/latency/error metrics; analytics is a separate, additive concern.

Decisions locked for the MVP:

- **Vendor: PostHog Cloud.** Fully usage-based, 1M events/mo free, unlimited
  seats, first-class JS **and Go** SDKs, and a self-host exit kept open. No
  vendor lock beyond a thin interface (below).
- **Server is authoritative for domain events.** Submit/merge/create and auth
  are emitted from Go, where they are truthful and adblock-proof. The web emits
  UI intent + pageviews only. Nothing is counted on both surfaces.
- **Clerk user id is the join key.** The same `distinct_id` (Clerk subject id)
  is used by web and server so events land on one person.
- **Off by default.** When the API key env var is unset (local, tests, CI) the
  client is a no-op. Analytics is dark-launchable — no key, no calls.

## Design principles

1. **One event taxonomy, two SDKs.** Event names and property keys are defined
   once (a Go const block and a mirrored TS const map). Web and server must not
   drift, or the joins break.
2. **Authoritative events server-side, intent events client-side.** The server
   sees the truth for `changeset_submitted` / `changeset_merged` /
   `slice_created`; the browser sees UI intent the server can't (clicks, views,
   navigation). Never double-count the same action on both surfaces.
3. **Non-blocking.** Analytics must never block or fail an RPC or a render.
   Server capture is buffered and flushed asynchronously; web capture is
   client-only and best-effort.
4. **Swappable backend.** All server emission goes through an
   `analytics.Client` interface, mirroring how `internal/metrics` wraps its
   sinks. Replacing PostHog (or adding a warehouse fan-out) is a one-file change.
5. **Privacy first.** No PII (Clerk email/name) in event properties unless
   explicitly intended. Region and consent decided before launch, not after.

## Identity model

- **Anonymous → identified.** Before login the web SDK uses an anonymous
  `distinct_id`. On login (`web/src/auth/ClerkAuthProvider.tsx`) we call
  `posthog.identify(clerkUserId)` and `posthog.alias()` so pre-login activity
  merges into the user.
- **Server distinct id.** Server events use `authctx.SubjectID(ctx)` (the Clerk
  subject id) as `distinct_id` — identical to the web's identified id, so the
  two streams unify with no extra mapping table.
- **CLI.** `gs` actions that hit authenticated RPCs are attributed server-side
  by the same subject id automatically (no separate CLI SDK needed for v1).

## Server design (`internal/analytics`)

New package, shaped like `internal/metrics`:

```go
package analytics

type Event struct {
    Name       string
    DistinctID string
    Props      map[string]any
}

type Client interface {
    Capture(ctx context.Context, e Event)
    Identify(ctx context.Context, distinctID string, props map[string]any)
    Close() error
}
```

- **`posthog` impl** wraps `github.com/posthog/posthog-go`, buffered with async
  flush; `Capture` never blocks the caller.
- **`noop` impl** returned when `POSTHOG_API_KEY` is empty — used by tests,
  local dev, and CI. This is the default.
- **Construction lives in `server/`** (wiring-only per AGENTS.md); services
  receive the `analytics.Client` interface, never the concrete type.

### Emission points

- **Interceptor (coarse, opt-in).** In `server/observability.go`, alongside
  `grpcMetricsUnaryInterceptor`, an analytics interceptor emits an event only
  for an allowlisted set of domain-event methods (not every RPC — that is noise
  and cost). Pulls the actor from `authctx.SubjectID(ctx)`.
- **Service-layer (precise).** The authoritative domain events are emitted in
  `service/` at the point the write succeeds:
  `slice_created`, `changeset_submitted`, `changeset_merged`,
  `patchset_pushed`, `auth_login`, `cli_login_completed`.

## Web design

- `posthog-js`, initialized **client-side only** (guard `typeof window`) — the
  app is TanStack Start with SSR, so init happens in the hydrate path
  (`web/src/client.tsx`) or a mount effect, never during SSR.
- A `PostHogProvider` mounted in `web/src/start-routes/__root.tsx`, keyed off
  `import.meta.env.VITE_POSTHOG_KEY` / `VITE_POSTHOG_HOST`.
- **Autocapture + pageviews** on: TanStack Router navigation fires
  `$pageview`.
- **UI-intent events only** via `posthog.capture(...)`. Domain events
  (submit/merge/create) are NOT captured here — the server owns them.

## Event taxonomy (v1)

| Event | Emitted by | Key props |
|---|---|---|
| `$pageview` | web | route path |
| `slice_viewed` | web | slice id |
| `slice_created` | **server** | slice id |
| `changeset_submitted` | **server** | changeset id, slice id |
| `changeset_merged` | **server** | changeset id, slice id |
| `patchset_pushed` | **server** | changeset id, patchset id |
| `auth_login` | **server** | method |
| `cli_login_completed` | **server** | — |

Names and prop keys are declared once in Go (`internal/analytics/events.go`)
and mirrored in `web/src/analytics/events.ts`.

## Ingestion / anti-adblock

Ingestion is proxied first-party through Cloudflare (`/ingest/*` on the web
domain → PostHog ingestion host) so events survive adblockers. This is a
Cloudflare route/Worker config, not app code. `VITE_POSTHOG_HOST` points at the
first-party path.

## Configuration

| Surface | Var | Where |
|---|---|---|
| Web | `VITE_POSTHOG_KEY`, `VITE_POSTHOG_HOST` | `web/wrangler.jsonc` per-env `vars` |
| Server | `POSTHOG_API_KEY`, `POSTHOG_HOST` | Cloud Run env |

Unset key ⇒ no-op client. Use **separate PostHog projects for staging and
production** so `agenttools.dev` test traffic does not pollute `gitslice.io`
analytics.

## Rollout plan

1. **Foundation** (one codex worktree): `internal/analytics` (interface +
   posthog + noop impls) and the shared event taxonomy (Go + TS). Verify it
   builds; no emission wired yet.
2. **Fan out** (two parallel codex worktrees, disjoint paths):
   - Server: interceptor + service-layer domain events (`server/`, `service/`).
   - Web: `PostHogProvider`, pageviews, identify/alias, UI-intent events
     (`web/`).
3. **Config + proxy** (main agent): wrangler vars, Cloud Run env, Cloudflare
   `/ingest/*` route.
4. **Verify** on staging: drive login → view slice → submit changeset; confirm
   events appear as **one stitched person** and submit/merge are **not**
   double-counted.
5. **Backstop:** set a per-product billing limit in PostHog. Ship to prod behind
   env vars (dark-launchable).

## Non-goals (v1)

- Engineering observability / tracing — stays on Prometheus + `internal/metrics`.
- Session replay, feature flags, experiments, surveys — available in PostHog but
  out of scope until the event pipeline is proven.
- A separate CLI analytics SDK — CLI actions are attributed server-side.
- Warehouse fan-out / CDP — the `analytics.Client` interface leaves room, but
  not built now.
