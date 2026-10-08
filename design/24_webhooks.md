# 24. Webhooks

Status: built 2026-10-08.

## Goal

External services should act on what happens in a slice without polling.
The first user is the release build. It used to ask Cloud Build every 15
minutes whether a new tag existed. Now the tag's own `tag.created` event starts
the build (`design/21_self_hosting.md`, "Releases Off GitHub").

A webhook belongs to a slice. It holds:

- an HTTPS URL;
- the events it wants;
- an optional secret;
- an active flag.

When a subscribed event happens, Gitslice POSTs a JSON body to the URL. Only
the slice's owners and admins see or change its webhooks, because a URL can
carry a token.

## Events

| Event | When | Main payload fields |
| --- | --- | --- |
| `push` | A commit landed that changes paths this slice includes, whichever slice authored it | `commit` (id, target ref, the changed paths inside this slice) |
| `tag.created` | A tag was created (re-creating the same tag is not an event) | `tag` (name, commit id, message) |
| `changeset.created` | A changeset was opened in the slice | `changeset` |
| `changeset.updated` | A new patchset was uploaded | `changeset` |
| `changeset.approved` | A changeset was approved | `changeset` |
| `changeset.submitted` | A changeset landed | `changeset`, `commit` (all changed paths) |
| `changeset.abandoned` | A changeset was abandoned | `changeset` |
| `check_run.completed` | A check run or a reported check result finished | `changeset`, `check_run` |
| `ping` | Someone asked for one (`gs webhook ping`, or Ping in the UI) | `hook`, `zen` |

`*` subscribes to every event. Ping is always delivered, even to an inactive
webhook, so you can test one before switching it on.

### Payload

Every body has the same envelope:

```json
{
  "id": "evt_…",
  "event": "tag.created",
  "created_at": "2026-10-08T16:00:00Z",
  "slice": {"id": "slice_…", "account": "gitslice", "name": "gitslice",
            "full_name": "gitslice/gitslice", "url": "https://gitslice.io/slices/gitslice/gitslice"},
  "sender": {"username": "nic"},
  "tag": {"name": "v0.5.0", "commit_id": "sha256:…", "message": "Release v0.5.0"}
}
```

A `changeset` section has these fields:

- `id`, `handle`, `number`, `title`, `status`;
- `author` (a username);
- `target_ref`, `patchset_id`, `patchset_number`, `commit_id`;
- `url` (`https://gitslice.io/cs/<handle>`).

A `push` to another slice leaves out the changeset. It describes only the
commit, and only the paths that slice includes.

Events hold references, not copies. The payload is built when the event is
fanned out, from the current changeset, slice and check run. It is stored with
the delivery, so retries and redeliveries send the same bytes.

### Headers

| Header | Value |
| --- | --- |
| `Content-Type` | `application/json` |
| `User-Agent` | `Gitslice-Webhook/1.0` |
| `X-Gitslice-Event` | the event name |
| `X-Gitslice-Event-ID` | the event id: the same on retries and redeliveries |
| `X-Gitslice-Delivery` | this delivery's id |
| `X-Gitslice-Hook-ID` | the webhook's id |
| `X-Gitslice-Signature-256` | `sha256=` and the hex HMAC-SHA256 of the body, keyed by the secret (only when a secret is set) |

To verify a delivery, compute the HMAC of the raw body and compare it in
constant time:

```go
mac := hmac.New(sha256.New, []byte(secret))
mac.Write(body)
ok := hmac.Equal([]byte("sha256="+hex.EncodeToString(mac.Sum(nil))), []byte(r.Header.Get("X-Gitslice-Signature-256")))
```

`webhooks.Verify` in `internal/webhooks` does the same.

## Delivery

1. **Record.** Producers append an event to `webhook_events`:
   - Thin wrappers around the changeset, check and slice stores
     (`internal/webhooks/events.go`) record the others. Every path that
     creates a changeset, patchset, tag or check result goes through them,
     including Git pushes.
   - `changeset.submitted` is written by the publisher, in the same
     transaction that lands the commit.
2. **Fan out.** The dispatcher claims recorded events and creates one
   `webhook_deliveries` row per matching webhook, in one transaction:
   - A slice's webhooks get the slice's events.
   - A `changeset.submitted` also becomes a `push` for every webhook whose
     slice includes a changed path. Its event id is `<event id>.push`.
3. **Send.** Each due delivery is POSTed:
   - The request times out after 10 seconds.
   - Redirects are not followed.
   - Any 2xx response is success.
   - The first 2 KiB of the response body is kept.
4. **Retry.** A failed attempt is tried again after 1 minute, 5 minutes,
   30 minutes, 2 hours, 6 hours and 12 hours. After 7 attempts the delivery is
   `failed`.
   - These failures are final at once: a refused address, a deleted or
     inactive webhook, and a ping.
   - `gs webhook redeliver` (or Redeliver in the UI) sends a delivery's body
     again, now, as a new delivery with the same event id.

Delivery is at least once and unordered. Receivers should drop duplicate event
ids and should not rely on order (for example, `changeset.created` may arrive
after `changeset.submitted`).

### Delivering on a throttled host

Production runs on Cloud Run with CPU throttled between requests, so
background goroutines barely run. The request that causes an event therefore
delivers it itself:

- It records the event, then waits up to two seconds while the dispatcher fans
  out and sends.
- A slow receiver does not hold the request longer; the send continues in the
  background.
- When no webhook is active (the answer is cached for 30 seconds), the request
  does not wait at all.

A background loop drains every 30 seconds, after the publisher lands
commits, and at startup. It handles retries and anything an inline drain left
behind. On a host scaled to zero, a retry happens at the next start.

Fanned-out events and finished deliveries older than 30 days are pruned
hourly.

## Safety

- **Who:**
  - Creating, listing, changing, pinging and reading the deliveries of a
    webhook all need the admin role on the slice's account.
  - A slice can have at most 20 webhooks.
- **Where:**
  - URLs must be `https://`, have no user part, and be at most 2048 bytes.
  - Deliveries go only to public internet addresses. Loopback, private,
    link-local (including the cloud metadata address), carrier-grade NAT and
    other reserved ranges are refused, both when the URL is saved and, after
    DNS resolution, when it is dialed.
  - `GITSLICE_WEBHOOK_ALLOW_PRIVATE_TARGETS=1` lifts these rules for tests and
    local development only.
- **Secrets:**
  - They are sealed with the server's secrets key (`GITSLICE_SECRETS_KEY`),
    like slice secrets, and are never returned.
  - The API reports only `has_secret`.
  - A URL's query often carries a token. The CLI's text output and the web
    list show query values as `...`; `--json` and the edit form show the URL
    in full.

## API, CLI and UI

`WebhookService` (`proto/core/v1/webhook.proto`) is served over gRPC and
Connect:

- `CreateWebhook`, `ListWebhooks`, `UpdateWebhook`, `DeleteWebhook`;
- `PingWebhook`;
- `ListWebhookDeliveries`;
- `RedeliverWebhookDelivery`.

The CLI:

```
gs webhook create [--slice account/slice] --url https://… --event tag.created [--event push] [--secret-stdin] [--inactive]
gs webhook list [--slice account/slice]
gs webhook update <webhook-id> [--url …] [--event …] [--active true|false] [--secret-stdin | --clear-secret]
gs webhook ping <webhook-id>
gs webhook deliveries <webhook-id> [--limit n]
gs webhook redeliver <delivery-id>
gs webhook delete <webhook-id> --yes
```

`--slice` defaults to the workspace's slice. Every command takes `--json`.

The web UI is the Webhooks panel in Slice Settings, shown to the account's
owners and admins. From it you can:

- add, edit, enable, disable and delete webhooks;
- remove a secret;
- ping a webhook;
- browse recent deliveries, with their request and response bodies, and
  redeliver one.

## Server configuration

| Variable | Meaning |
| --- | --- |
| `GITSLICE_WEB_BASE_URL` | Origin for links in payloads; defaults to the first allowed CORS origin |
| `GITSLICE_WEBHOOK_ALLOW_PRIVATE_TARGETS` | `1` allows http and private addresses (tests, local development) |
| `GITSLICE_SECRETS_KEY` | Seals webhook secrets (already required in production) |

## Use: the release build

`ops/release/release.sh webhook` sets up the Google Cloud side:

- It creates a Cloud Build webhook trigger, `gs-release-webhook`, that runs
  `ops/release/cloudbuild.yaml` with `_TAG=$(body.tag.name)`. Its filter skips
  every event but `tag.created`.
- It writes the trigger's URL, which carries an API key limited to Cloud
  Build and the trigger's secret, to a local file.

The `gitslice/gitslice` slice then has one webhook:

```
gs webhook create --slice gitslice/gitslice --event tag.created --url "$(cat ~/.config/gitslice-release/webhook-url)" --quiet
```

`build.sh` builds only a `vX.Y.Z` tag newer than `_SINCE` that is not
published yet, so a repeated or stray event builds nothing. A daily Cloud
Scheduler run (`release.sh schedule`) builds any tag whose event was missed.

## Not yet

- Account-level and organization-level webhooks. Today each slice has its own.
- Per-event payload filters beyond event names, such as only `push` to certain
  paths.
- Automatic deactivation of endpoints that keep failing.
