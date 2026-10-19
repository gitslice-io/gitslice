# 32. CI dispatch on Cloudflare: doorbell, runners, pool

Status: design (2026-10-19), nothing built yet. Builds on design/16 (daemons),
design/17 (checks) and design/31 (the CI daemon). The detach-and-poll change
described in design/31 ("Idle daemons and the server bill") is the foundation
and is assumed here.

## Problem

1. **An idle CI daemon is billed all day.** A daemon holds `Connect` open, and
   Cloud Run bills while any request is open. Measured 2026-10-19: one CI
   daemon was 23.5 of the 28 long-request hours a day, about 25 billed
   instance-hours a day, roughly $65 a month, for a daemon that runs checks a
   few times a day.
2. **Detach-and-poll fixes the bill, not the instance count.** A poll every 30 s
   keeps the instance warm (Cloud Run keeps an idle instance about 15 minutes),
   but an idle warm instance is not billed. Reaching zero instances needs
   something outside Cloud Run to wake the daemon.
3. **CI runs on one machine.** 2026-10-19: its disk filled (the e2e check's
   Postgres crashed into recovery mode and the check failed with no code
   problem), it runs one check at a time (about 13 minutes per changeset), and
   when the slice's CI daemon was offline (grok-bot-ci, 2026-10-18) nothing
   could land.
4. **A slice has exactly one CI daemon**, so there is no failover.

## What Cloudflare offers (as of 2026-10)

Sources are listed at the end.

- **Queues.** 128 KB messages, retained up to 14 days, up to 100 retries, 10,000
  queues per account, 5,000 messages a second per queue. $0.40 per million
  operations (one per 64 KB written, read or deleted), 1 million a month
  included. *Push* consumers are Workers. *HTTP pull* consumers need an API token
  with `queues_read` and `queues_write` and only short-poll (they return at once,
  never hold the connection). A daemon on someone else's machine therefore cannot
  wait on a queue: it would need an account token.
- **Durable Objects with WebSocket hibernation.** An object holding idle
  WebSockets hibernates and accrues no duration charge; protocol ping/pong does
  not wake it; deploying new code disconnects every socket. $0.15 per million
  requests beyond the first million a month. SQLite-backed objects are available
  on the free plan.
- **Containers.** Generally available since 2026-04-13. Instances up to 4 vCPU,
  12 GiB and 20 GB of disk. CPU is billed per 10 ms of *active* CPU
  ($0.000020 per vCPU-second); memory ($0.0000025 per GiB-second) and disk
  ($0.00000007 per GB-second) while the instance runs. Docker-in-Docker is
  supported with the `docker:dind` image, run as root with iptables and IP
  forwarding off.
- **The `durable_object` scheduling policy** lets code choose each container's
  image and instance type at start time. Median start 648 ms (95th percentile
  910 ms). 100,000 containers were started in 5.4 s in Cloudflare's own test.
  The `@cloudflare/containers` helper class works only with the *default*
  policy, so this means the raw `ctx.container` API.
- **Snapshots** (public beta, `durable_object` policy only). A snapshot holds the
  writable root filesystem (files, installed packages, build caches), not memory,
  processes or mounted filesystems. Immutable, tied to the image version it was
  taken from, capped at 20 GB, kept 30 days from creation or last restore.
  Cloudflare describes sharing one snapshot as the baseline of many isolated
  environments, which is the shape CI needs.

## Goals

- An idle CI setup costs nothing: no Cloud Run instance, no running container.
- A queued check starts within a few seconds of being queued.
- The database stays the source of truth for check runs. Cloudflare only wakes
  things, so losing a wake-up costs a slow poll, never a lost check.
- No CI secrets (slice secrets) are stored in Cloudflare queues or objects.
- CI does not depend on one machine, and checks of a changeset run in parallel.
- A slice can name several CI daemons.

Not goals: changing `checks.yaml`; moving the Go server, its database or its
object store.

## Layer 1: the doorbell (Queue, Worker, Durable Object)

```
Cloud Run (Go server)                        Cloudflare
  check run created (queued)
        |  POST /ring (HMAC-signed)  ──────►  Worker `doorbell`
        |                                       └─ Queue `ci-wake`.send({daemon_id, run_id})
        |                                                   │
        |                                   consumer Worker ▼
        |                                       Durable Object (one per daemon)
        |                                         · has a hibernating WebSocket? send "wake"
        |                                         · none? remember "wake pending" in storage
        |                                                   │ wss
daemon (detached, idle) ◄─────────────────────────────────┘
  connects to Cloud Run (`Connect`) and receives the queued runs as it does today
```

**Messages carry ids only** (`daemon_id`, `run_id`, a timestamp). The runs, their
specs and the slice secrets stay in Postgres and travel over `Connect` as
today. A wake-up is idempotent: waking an awake daemon, or twice, does nothing.

**Daemon side.** A detached daemon opens a WebSocket to the doorbell instead of
polling every 30 s, and sends a ping every 30 s (answered without waking the
object). It still calls `StandbyPoll`, but only as a safety net, every 20 to 30
minutes. If the WebSocket is down it falls back to polling every 30 s, which is
the detach-and-poll behaviour that exists today. Cloud Run then sees a request
every half hour at most, so the instance spends most of the day at zero.

**Authentication.** Cloud Run mints a *doorbell token* (signed with Ed25519: the
daemon id, an expiry of about 24 h) and returns it in `StandbyPollResponse` and
in the `Connect` registration. The daemon presents it when opening the
WebSocket; the Worker verifies it with a public key and needs no call back to
Cloud Run (which would wake it). The token is refreshed by the safety poll and
whenever the daemon reconnects. It can only receive wake-ups for its own daemon.
The server rings the doorbell with an HMAC-signed request whose secret lives in
Secret Manager. No Cloudflare API token ever reaches a daemon.

**Why a queue and not a direct call.** The server could call the Durable Object
through the Worker directly, and that works. The queue adds a durable buffer and
retries if the object is briefly unavailable (including the moment a deploy
drops every WebSocket), a dead-letter queue and counters for debugging, and
keeps the Cloud Run request short (enqueue, then return). It costs $0.40 per
million operations, which at our volume is nothing. If this proves
unnecessary, dropping it leaves the design intact.

**Failure modes.**

| Failure | Effect |
|---|---|
| Doorbell or Cloudflare down | Daemons fall back to 30 s polls (Cloud Run stays warm and billed per poll). No check is lost. |
| Wake-up lost or late | The safety poll finds the queued run within 30 minutes; the 30 s fallback applies while the socket is down. |
| Daemon offline when rung | The object stores "wake pending"; the daemon gets it when its socket opens. |
| Worker deploy disconnects sockets | Daemons reconnect with backoff and poll meanwhile. |
| Token stolen | It can only wake one daemon, for under a day. |

**Cost.** Expected to be cents a month: one request per wake, no duration
charge while hibernating, and pings that do not wake the object. How
Cloudflare bills incoming WebSocket messages (the documentation I read does not
say) is one of the things spike 0 counts.

## Layer 2: Cloudflare-hosted runners (Containers and snapshots)

The doorbell can start a container instead of waking a daemon someone runs.
Then there is no CI machine to keep alive, and the checks of a changeset run in
parallel.

```
wake message ──► Durable Object `runner-pool`
                   · decides how many runners to start (queued runs, max N)
                   · start({ image: CI image, instanceType: standard-4,
                             containerSnapshot: golden snapshot })
                         │
                    container: `gs agent start --ci --name cf-ci --exit-after-idle 30s`
                         · connects to Cloud Run, receives queued runs, runs them
                         · exits when it has been idle for 30 s
```

**The golden snapshot.** A start without a snapshot would download Go modules,
rebuild Go's compile cache, run `npm ci` and pull images every time (the cold
costs measured on 2026-10-18: build 7m38s, image 2.5 GB). So a *golden
snapshot* holds what is expensive and changes rarely:

- Go's module cache and build cache (the CI host's cache folder was 1.2 GB),
- `node_modules` for the web app,
- if Docker-in-Docker is used, `/var/lib/docker` with the CI, Node and Postgres
  images.

It is built by a runner (restore the previous one, run the slice's checks, take
a new snapshot) after a change to `go.mod`, `go.sum`, `package-lock.json` or
`ci/Dockerfile`, and at most daily otherwise. Because a snapshot is tied to one
image version, the key is *image digest + the hash of those input files*; a
mismatch means "start cold, then take a new golden". Snapshots are immutable,
so a runner never writes the golden one: each run restores it, changes its own
copy, and discards it. Snapshots live 30 days after their last restore, so a
daily restore keeps one alive.

**Two ways to run the checks inside a runner**, to be decided by the spike:

- **A. Docker-in-Docker.** The runner image is `docker:dind` plus `gs`. Checks run
  exactly as they do on any daemon today (`image`, `build`, `services` all
  unchanged). Needs root and iptables off, supported by Cloudflare, and the
  snapshot must hold the docker image store (about 4 GB of the 20 GB disk).
- **B. Native.** The runner image *is* the CI image (Go, Node, Postgres and `gs`)
  and checks run in it directly with the services as processes. Faster to
  start and no Docker, but needs a new check mode and a way to say which
  runner image a check wants; `build:` would build a *runner image* through
  Cloudflare's image pipeline rather than on the daemon.

**Identity and secrets.** A runner registers as the existing `gitslice-checks`
account with the agent credentials passed in as a Worker secret, as
environment variables. Slice secrets reach it only over `Connect`, in memory,
as with any daemon, and the container is discarded afterwards. Several runners
sharing one daemon name needs the pool support in layer 3.

**Parallelism and cost.** Today checks run one at a time (`GITSLICE_CHECK_CONCURRENCY=1`
on a 2-vCPU host): about 13 minutes per changeset. Six runners would take about
as long as the slowest check (e2e, about 5 minutes warm). Estimated cost per
changeset at standard-4 (4 vCPU, 12 GiB), assuming about 70% CPU busy:
e2e about $0.03, web build about $0.02, the other four about $0.03 together:
roughly **$0.08 to $0.10**, or $25 to $45 a month at 10 to 20 changesets a day.
That is not cheaper than a machine already paid for; the reasons to do it are
independence from one host, parallel checks, and no disk to fill. These are
estimates from list prices; the spike measures them.

## Layer 3: a pool of CI daemons, with leases

Needed for layer 2 (many runners, one identity) and valuable alone (failover).

- `slices.ci_daemon_id` becomes a set of daemons; `gs slice set-ci-daemon` adds
  or removes one.
- `check_runs` gains `claimed_by` and `lease_expires_at`. A daemon claims queued
  runs with `UPDATE ... WHERE id IN (SELECT ... FOR UPDATE SKIP LOCKED)` and
  extends its lease with each heartbeat and log chunk.
- A run whose lease expires goes back to `queued` and is claimed by another
  daemon, so a daemon that dies mid-check no longer strands it.
- The hub holds several connections per daemon id; a rung wake-up goes to every
  idle daemon of the pool, which race to claim.
- Check results are already idempotent per run id; a run claimed twice after an
  expired lease is guarded by the claim.

This is the semantics people expect of a queue (lease, visibility timeout,
redelivery). Doing it in Postgres, not in the Cloudflare Queue, keeps a single
source of truth and works with the gate and the UI unchanged; the Queue only
rings the bell.

## Security

- Messages hold no secrets; tokens hold only a daemon id and an expiry.
- The doorbell verifies, never decides: the server and the database still
  authorize every run, and `ciDaemonRefusal` (exposure) still applies at
  dispatch.
- Runners hold the agent credentials and, while a check runs, its slice
  secrets: the same trust as a daemon on a CI host, inside the owner's own
  Cloudflare account. Third parties' daemons are unchanged.
- The `/ring` endpoint takes only HMAC-signed requests with a timestamp, to
  refuse replays.

## Spike plan (before anything is built)

**Spike 0, the doorbell (about half a day).** A Worker, an object and a queue in
a scratch Cloudflare account, and a fake daemon behind NAT. Measure: wake
latency (queue to daemon, median and p95); whether an idle WebSocket survives
home NATs and hours (and what ping interval it needs); what a Worker deploy
does to connected daemons; billed requests per daemon per day.

**Spike 1, runners (about one day).** A container on the `durable_object`
policy running the real gitslice checks. Measure: whether `docker:dind` starts
and builds `ci/Dockerfile` (variant A); snapshot size and restore time for the
Go cache (1.2 GB) and for the docker store (about 4 GB); whether one snapshot
restores into six instances at once; start time of a standard-4 instance;
wall-clock and cost of a full changeset of checks against today's 13 minutes;
whether Postgres in a container behaves in dind.

**Decision gates.** Layer 1 proceeds if wake latency is a few seconds and idle
sockets hold. Layer 2 proceeds only if a changeset's checks finish faster than
today and snapshot restore is quick for the cache sizes above.

## Alternatives considered

- **Detach-and-poll alone.** Built (design/31). Cheap and simple; keeps one
  instance warm but unbilled. This is also the fallback for every layer here.
- **Daemons pulling from a Cloudflare Queue directly.** Needs an account token
  on every daemon, and short-polls. Rejected.
- **GCP Pub/Sub or Cloud Tasks.** Both push to an endpoint the daemon does not
  have (it is behind NAT), or need a pull with credentials. They do not remove
  the need for a connection to wake the daemon.
- **A Cloudflare Queue as the authoritative dispatch store.** Rejected: runs,
  gates, logs and the UI read Postgres, so two sources of truth would need
  reconciling. The Queue is a doorbell.
- **Cloud Run `min-instances` plus CPU always on.** Costs more, not less.

## Open questions

- Does an open daemon WebSocket behind carrier-grade NAT stay up with 30 s pings?
- Does `docker:dind` run in a Cloudflare container with the settings Cloudflare
  documents (root, iptables off), and how fast is overlay storage on the
  ephemeral disk?
- How long does restoring a 5 to 6 GB snapshot take, and can six instances
  restore one at once?
- Is the 20 GB disk enough for the golden snapshot plus a checkout plus test
  data?
- Do snapshots, in public beta, keep their semantics across image updates well
  enough to automate the golden build?
- Which Cloudflare plan is the account on (Queues, Durable Objects and Containers
  billing differ between free and paid)?

## Sources

- Cloudflare Queues [limits](https://developers.cloudflare.com/queues/platform/limits/),
  [pricing](https://developers.cloudflare.com/queues/platform/pricing/),
  [HTTP pull consumers](https://developers.cloudflare.com/queues/configuration/pull-consumers/)
- Durable Objects [WebSocket hibernation](https://developers.cloudflare.com/durable-objects/best-practices/websockets/),
  [pricing](https://developers.cloudflare.com/durable-objects/platform/pricing/)
- Containers: [limits](https://developers.cloudflare.com/containers/platform-details/limits/),
  [FAQ](https://developers.cloudflare.com/containers/faq/),
  [snapshots guide](https://developers.cloudflare.com/containers/guides/snapshots/),
  [Container class](https://developers.cloudflare.com/containers/container-package/),
  [rebuilt for agent sandboxes (6x faster starts, scheduling policy)](https://blog.cloudflare.com/faster-agent-sandboxes/)
