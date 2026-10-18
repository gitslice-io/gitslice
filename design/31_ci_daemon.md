# 31. CI Daemon: Run Anywhere, Build Once

Status: implemented (2026-10-18): `build` and `services` in checks.yaml
(`internal/checks`), image builds, services and user mapping in
`internal/checkexec`, `gs agent start --ci`, `gs agent doctor` and
`gs agent install` (`internal/cli/agent_doctor.go`, `agent_install.go`), daemon
status on check runs. Since v0.4.11 the gitslice slice's own checks run in
images built from the tree: Go checks in `ci/Dockerfile` (with a Postgres
service for e2e), the web check in `web/ci/Dockerfile`. Each rebuilds only
when its own dependency files change, and the web check stays inside a
web-only slice. Builds on design/17 (agent-run checks)
and design/16 (daemon exposure).

## Problem

On 2026-10-18 `gitslice:gitslice` switched its CI daemon to one started on
another machine. Its checks queued and never ran. Several things combined:

1. **Checks depend on the CI host.** The slice's checks ran on the host
   (no `image`), so they needed that host's Go and Node toolchains. Its e2e
   check used the slice secret `GITSLICE_TEST_DATABASE_URL`, which points at a
   Postgres on the original CI host's loopback, so on any other machine it
   points at nothing.
2. **Container checks never worked on rootful Docker.** The daemon
   materializes a check's tree in a 0700 temp directory owned by its own user,
   and ran the container as root with every capability dropped. Root without
   `CAP_DAC_OVERRIDE` cannot read a directory it does not own, so the check
   could not read or write `/workspace`. Only rootless Docker or Podman worked.
3. **A daemon is a foreground process.** `gs agent start` dies with its
   terminal or the machine, and nothing restarts it. Our own CI daemon survives
   only because pm2 runs it. It also requires an empty directory and the
   `codex` runtime even when it only runs checks.
4. **Nobody is told.** While the slice's CI daemon is offline, its checks sit
   `queued` and `gs create --submit` waits silently until its timeout.
5. **The daemon claims what it cannot do.** It reports a container runtime
   whenever a `docker` binary is on the PATH, even when the Docker daemon is
   not running or the user may not talk to it.

## Goals

- A CI daemon on a new machine needs `gs` and Docker (or Podman), nothing
  else, and starts with one command that keeps it running.
- A check's environment comes from the tree: an image built from a Dockerfile
  in the slice, plus service containers (a database) the check declares.
- The image is built once per distinct Dockerfile and inputs, and reused by
  every later run, on every patchset, until those inputs change.
- When checks are waiting on an offline daemon, everyone waiting is told.

## Images built from the tree

A check, or a file's `defaults`, may declare `build` instead of `image`:

```yaml
defaults:
  build:
    dockerfile: ci/Dockerfile        # relative to context
    context: "."                     # optional; default the checks.yaml folder;
                                     # leading "/" = repo root
    inputs:                          # files and folders the build may read,
      - go.mod                       # relative to context; the Dockerfile is
      - go.sum                       # always one
      - web/package.json
      - web/package-lock.json
    timeout: "30m"                   # optional; default 30m
```

`image` and `build` are mutually exclusive; `setup` may follow either.

**Resolution (server).** The resolver turns `context`, `dockerfile` and
`inputs` into repo-relative paths, rejects `..`, and adds the Dockerfile and
every input to the check's materialization, so the daemon has them; they count
for `OutOfSlice` like `include`. The `CheckRunSpec` carries them as
`ImageBuild{context_dir, dockerfile, inputs, timeout_ms}`.

**Key.** The daemon hashes, from the materialized tree, the Dockerfile path
and every input file (relative path, executable bit and content, sorted; a
folder means every file under it). The image is
`gitslice-ci/build-<family>:<key>`, where the family hashes the slice and the
Dockerfile path. A run whose key already exists locally (`image inspect`) uses
it at once: no build, no context upload. This is the cache the issue asked
for: Go modules and `node_modules` installed in the image are reused by every
run until `go.sum` or `package-lock.json` changes.

**Build.** On a miss the daemon copies only the Dockerfile and the inputs into
a fresh context directory and runs `docker build`. Docker's own layer cache
still applies, so a change to `package-lock.json` rebuilds only the layers from
the one that copies it. Checks that need the same key at the same time wait for
one build (single flight per daemon). A failed build fails the check as
`errored` with the build log. Each image is labelled with its family; after a
build the daemon removes all but the two newest images of that family, so disk
use stays bounded.

**Reported.** The check log starts with one line saying which image ran and
whether it was built (and how long that took) or reused.

## Services

A file may declare service containers, and a check names the ones it needs:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    env: { POSTGRES_HOST_AUTH_METHOD: trust }
    ready: "pg_isready -U postgres -h 127.0.0.1"   # retried until it passes
    ready_timeout: "60s"                           # default 60s
    memory: "1g"                                   # default 1g

checks:
  e2e:
    services: [postgres]
    env:
      GITSLICE_TEST_DATABASE_URL: "postgres://postgres@postgres:5432/postgres?sslmode=disable"
```

Services need a containerized check (`image` or `build`). For each run the
daemon creates a private network (`--internal` unless the check sets
`network: true`), starts every service on it under its name as the host name,
runs its `ready` command inside it until it passes, then runs the check on the
same network. The services and the network are removed when the run ends,
whatever the outcome. A service that never becomes ready errors the check with
the end of its logs. Nothing about a service survives the run, so every run
gets an empty database.

## Running containers as the daemon's user

On rootful Docker the check container runs as the daemon's uid and gid
(`--user`), so it owns the materialized tree as the daemon does. Rootless
Docker already maps container root to the daemon's user and stays as it is;
rootless Podman uses `--userns=keep-id`. The daemon detects which applies once
per process from `docker info` / `podman info`.

`cache` paths become bind mounts of folders under the daemon's cache
directory (`$XDG_CACHE_HOME/gitslice/check-cache/<hash>`) instead of named
volumes, so they are writable by that uid and visible to the user who has to
free disk. The hash covers the slice and the path: the checks of one slice
that cache the same path share the folder (one Go build cache serves vet,
test and build), and two slices on one daemon never share one.

## A CI-only daemon

`gs agent start --ci` runs checks only:

- no conversations: it registers runtime `ci`, the server refuses to start a
  conversation on it, and the web app does not offer it;
- no agent runtime and no empty-directory requirement;
- at start it runs the same preflight as `gs agent doctor` and prints what is
  missing.

`gs agent doctor` reports: signed in and as whom; the container runtime, and
whether its daemon answers; rootful or rootless; free disk where checks
materialize and where caches live; host toolchains (Go, Node, git) for checks
that run on the host. The daemon registers a container runtime only when
`docker info` (or `podman info`) succeeds.

`gs agent install --ci --name <name>` makes the daemon a user service that
starts at login (or boot, with lingering) and restarts when it exits:

- Linux: a systemd user unit `gs-agent-<name>.service`, enabled and started;
  it says to run `loginctl enable-linger` when lingering is off.
- macOS: a LaunchAgent `io.gitslice.agent.<name>.plist`, bootstrapped.
- `--print` writes the unit to stdout instead; `gs agent uninstall <name>`
  stops and removes it.

So bringing up CI on a new machine is:

```sh
gs login
gs agent install --ci --name my-ci
gs agent expose my-ci acme:web
```

## Telling people a daemon is offline

`CheckRun` gains `daemon_online` and `daemon_last_seen_at`, filled by
`ListCheckRuns` / `GetCheckRun` from the hub and the daemon record.

- `gs create --submit` and `gs submit --watch`, while waiting on a required
  check, look up its run (at most every 30 s) and say once when its daemon is
  offline: `CI daemon grok-bot-ci is offline (last seen 18:56 UTC, 23m ago);
  "test" starts when it reconnects (or when the slice names another CI
  daemon)`.
- A server that predates these fields leaves `daemon_last_seen_at` empty, and
  clients then say nothing rather than call every daemon offline.
- `gs cs capture` no longer bundles a check this machine cannot run (no
  container runtime): bundling its error would keep the CI daemon from running
  it.
- The web checks panel shows the same for queued runs.

## Not in this change

- **More than one CI daemon per slice.** A slice still names one daemon. A
  pool (any online daemon exposed to the slice picks up its queued runs) is the
  natural next step and needs `slices.ci_daemon_id` to become a set plus
  re-routing queued runs whose daemon went away.
- Pushing built images to a registry so several daemons share them.
- Host-mode isolation beyond what design/17 already says.

## Rollout

1. Land the daemon and server changes; deploy the server (it must parse
   `build` and `services` before any `checks.yaml` uses them); release `gs`.
2. Upgrade the CI daemons.
3. Switch `gitslice/gitslice`'s checks to `build` + a Postgres service, and
   drop the `GITSLICE_TEST_DATABASE_URL` slice secret. The e2e check sets the
   URL in its command rather than under `env`, because a slice secret of the
   same name wins over `env`.
