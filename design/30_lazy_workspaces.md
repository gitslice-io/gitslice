# 30. Lazy Workspaces: `gs mount`

Status: implemented (2026-10-18). Phases 1 to 3: `internal/lazyfs`, the CLI's
edit source, and a writable `gs mount` whose edits stay local until `gs create`
(`internal/lazyfs/fusefs`, `internal/cli/mount*.go`), with `gs restore` and
`gs mount discard`. Phase 4: `gs sync`, `gs submit` and changeset switching in
a mount (`internal/cli/mount_layers.go`), `gs mount --prefetch`, macOS through
macFUSE (built, not yet run on a Mac), passthrough where the kernel allows it,
and `gs agent start --lazy`. Measurements are at the end.

## Goal

Open a slice of any size in about a second, read files only when something
reads them, and keep every edit on the local machine until the user creates a
changeset.

`gs init` checks out every file a slice includes. Since the streamed `Checkout`
(design/28) that takes 2.7 s for `gitslice/gitslice` (779 files) from a cold
cache, but it still grows with the slice, so a slice of 100,000 files means
minutes and gigabytes before the first edit. A lazy workspace pays for the
listing up front and for each file's content on first read.

The second requirement is as important as the first: **a lazy workspace never
sends an edit anywhere on its own.** Writing, renaming or deleting a file
changes a local layer only. Content leaves the machine when, and only when, the
user runs `gs create` or `gs modify` (or a tool they configured runs it for
them, see "Agents" below).

## Non-goals

- Windows. Linux comes first, then macOS.
- A `.git` directory in the mount. Git users keep `git clone`.
- Sharing one mount between users (`allow_other`), or between machines.
- Changing the server, the storage model or any RPC. Everything here is client
  side and uses the read RPCs that exist (`Checkout`, `ReadFiles`,
  `ReadBlobStream`, `ListDirectory`) and the write path `gs create` already uses.

## User experience

```bash
gs init --lazy gitslice/gitslice ~/src/gitslice   # about 1 s; no file content yet
cd ~/src/gitslice
cat gitslice/gitslice/README.md               # fetched on first read, then cached
$EDITOR gitslice/gitslice/service/slice.go    # edit stays in the local layer
gs status                                     # lists local edits; reads nothing it does not need
gs diff                                       # local only
gs create --message "service: ..."            # now, and only now, content is uploaded
gs sync                                       # moves the base; untouched files cost nothing
gs down; gs up                                # switch changesets: references, nothing fetched
gs unmount ~/src/gitslice                     # edits survive; gs mount ~/src/gitslice picks them up again
```

- `gs init --lazy <slice> [dir] [--prefetch <glob>...] [--read-only]` creates a
  lazy workspace and mounts it at dir (default: the current, empty directory,
  after which the shell that ran it must `cd "$PWD"` to leave the directory
  underneath the mount). `gs mount <dir>` mounts an existing one again after an
  unmount or a reboot, and `gs mount <slice> <dir> [--foreground]` still creates
  one, as an alias of `init --lazy`. The directory must be empty and not inside a
  `gs init` workspace or another mount (its `.gs` would hide theirs). A directory that
  cannot be used is refused before anything is made, so it leaves nothing in
  `gs mount list`. Mounting the same directory again re-attaches
  the same workspace (found by the mount path in its `mount.json`), edits
  included. `--prefetch` patterns (relative to the mount) are kept in
  `mount.json`: the daemon fetches matching files in the background when it
  mounts and after every base move.
- `gs unmount <dir>` flushes and unmounts. Local edits are kept.
- `gs fetch [path...]` warms the cache for paths (default: everything), for work
  offline or before a build.
- `gs mount status [--json]` reports the base commit, the local edits, cache
  hits and misses, bytes fetched, fetches in flight and the last error.
- `gs restore <path>... | --all` drops local edits (new in this design; useful
  for eager workspaces too).
- `gs mount list [--json]` lists every lazy workspace on the machine: mounted
  ones, and unmounted ones that still hold local edits, so none is forgotten.
- `gs mount discard <dir|id>` deletes an unmounted workspace and its local
  edits, after asking (or with `--yes`).

Every other workspace command works inside a mount unchanged: `status`, `diff`,
`create`, `modify`, `submit`, `sync`, the stack commands (`switch`, `up`,
`down`, ...), `ci`, `blame`.

## Design

### Two layers

A mounted workspace shows one directory tree made of two layers:

- **Base layer (read-only):** the slice at the base commit. Its listing is the
  manifest `Checkout` returns without content: every path with its kind, mode,
  size and content hash. It is immutable for a given commit, so the kernel may
  cache it freely. File content comes from the shared content-addressed client
  cache (`~/.cache/gitslice`), filled on demand and checked against its hash.
- **Local layer (read-write):** everything the user changed, on local disk only.

The merged view is the local layer over the base layer, as in overlayfs. The
local layer holds, per path:

| Entry | Meaning |
| --- | --- |
| file | content in a real file under `local/files/` |
| reference | a file whose content is a known hash (`content_hash`, `mode`), with no local copy: a base file that was renamed, or a changeset's version of a file after `gs switch` |
| directory | created or made opaque (hides the base directory's entries) |
| symlink | target stored in the entry |
| whiteout | deleted: hides the base path |

References are what keep renames and changeset switches from downloading
anything: renaming a 2 GB base file records a reference at the new path and a
whiteout at the old one. Its content is fetched only if someone reads it.

The entries live in `local/index`, an append-only journal of operations,
fsynced at the points where the file system promises durability (`fsync`,
`close` of a written file, `rename`, `unlink`), and compacted on mount.
Content lives in ordinary files, so a crash loses at most what the application
had not yet synced, as on any disk.

### Where things live

```text
~/.local/share/gitslice/mounts/<id>/      id: random, fixed when the workspace is created
  mount.json       slice, mount path, created and last-mounted times (what gs mount list reads)
  gs/              the workspace's .gs (slice.json, state.json, base snapshot, conflicts)
  manifest-<commit> base layer listing of each base commit in use (the
                   previous one is kept for one more move, for a command that
                   read mount.json just before it)
  local/index      local layer journal
  local/files/     content of locally written files
  control.sock     the mount daemon's control socket
  daemon.log
```

The mount serves `<dir>/.gs` as a pass-through of `gs/`, so every command finds
its workspace as it does today. Nothing under `.gs` is ever part of an edit
(changeset 435a3c7d75 already skips `.gs` at any depth).

### Multiple workspaces

Each lazy workspace is its own mount, as each `gs init` workspace is its own
directory today:

```bash
gs init --lazy gitslice/gitslice ~/w/fix-auth      # workspace 1
gs init --lazy gitslice/gitslice ~/w/readme-page   # workspace 2, same slice
gs init --lazy acme/payments    ~/w/payments       # workspace 3, another slice
```

Every mount is independent: its own local layer, base commit, active changeset
and stack, and `gs sync`. An edit in one is not seen in another until it lands
and the other syncs.

What they share is the content cache, which is keyed by content hash. A second
mount of a slice serves every file the first one read without a fetch, and
nothing is stored twice:

| | `gs init` workspace | `gs mount` workspace |
| --- | --- | --- |
| Create | copies every file (2.7 s for `gitslice/gitslice`, growing with the slice) | fetches the listing, about 1 s at any size |
| Disk | a full copy each | the workspace's edits and its listing |
| Another workspace of the same slice | downloads or copies everything again | reads the shared cache |

So ten parallel workspaces of one slice cost ten listings and whatever each one
edits, which makes one workspace per task, or per agent, cheap.

A workspace's identity is its state directory, not the mount point being
mounted: `gs mount list` shows each one with its id, slice, mount path, number
of local edits and whether it is mounted, and `gs mount --workspace <id> <dir>`
re-attaches one at another path (after a move, or a reboot that left it
unmounted). A workspace with local edits is never deleted implicitly; only
`gs mount discard` deletes one.

All of a user's mounts are served by one background process, the mount
supervisor (`gs mountd`, hidden), which the first `gs mount` starts and which
exits a minute after the last workspace is unmounted (`GS_MOUNTD_IDLE`
changes the minute). Each workspace is still its own kernel mount with its own
layers, fetcher and control socket in that process, so one workspace's edits,
errors and `unmount` never touch another's, and every command that talks to a
workspace's mount (`status`, `create`, `restore`, `sync`) is unchanged. The
supervisor has one socket of its own, per state directory, only to attach
workspaces. What it costs is isolation: a crash of the process drops every mount
(the dead ones show "transport endpoint is not connected" and `gs mount <dir>`
clears and re-attaches them), and a new `gs` binary takes effect for mounts only
when the supervisor next starts, so after `gs upgrade` unmount everything, or
wait for it to exit. Workspaces still hold their edits on disk throughout.
`gs mount --foreground` serves one workspace in its own process, as before, and
mounts made by older versions keep their own daemons until unmounted.

**Later: one mount, many workspaces.** One mount could hold many workspaces as
directories (`~/gs/<name>/...`), with `gs workspace new <name> <slice>` creating
one instantly and no new mount. That suits the agent daemon, which makes a
workspace per conversation and would otherwise make a mount per conversation,
and platforms where mounts are heavy or scarce (macOS, containers that allow
one FUSE mount). It is not in the phases below; see the open questions.

### The mount daemon

`gs mount` hands each workspace to the mount supervisor (or serves it in its
own process with `--foreground`). It serves file system requests and nothing else:

- It is built with a read-only client: its gRPC connection has an interceptor
  that refuses every method but `Checkout`, `ReadFiles`, `ReadBlobStream` and
  `ListDirectory`, and a test holds it to that. **There is no code path from a
  file system operation to an upload.**
- `lookup`, `getattr` and `readdir` are answered from the manifest and the
  local index, with no network.
- `open`/`read` of a base file serves the cached object. On a miss it fetches
  the file, and small siblings in the same directory (under 64 KiB, at most 256
  files or 4 MiB, as `gs init` batches today), with `ReadFiles`; files over 4 MiB
  stream through `ReadBlobStream`. Fetches for the same hash are shared. A read
  waits for its content. A dropped connection is retried for a few seconds
  (a fetch starts over, and only fetches what the cache still lacks); a fetch that gives
  up on an unreachable server makes the next ones fail at once for five seconds,
  so a program reading many uncached files offline is not held up by each. A
  read that fails, after 60 s at most, gets `EIO` and `gs mount status` says
  which path and why.
- The daemon does not wait for the server when it starts: it connects when the
  first fetch needs it. A mount, a remount, and every read the cache can
  answer work offline; `gs fetch` ahead of time is what makes a tree readable
  offline. Creating a new mount needs the server, for its listing.
- The first write to a base file copies it up: it fetches the content unless the
  file is opened with `O_TRUNC`, writes `local/files/<path>`, and records a file
  entry. `create`, `mkdir`, `unlink`, `rmdir`, `rename`, `symlink`, `chmod` and
  `truncate` change the local layer only.
- Hard links and extended attributes are refused (`EPERM`, `ENOTSUP`), as the
  native model has neither. File locks are local to the machine.
- Kernel caching: base entries and pages are cached until the base changes
  (long attribute and entry timeouts, `FOPEN_KEEP_CACHE`); local files use
  short timeouts and, on Linux 6.9 and later, FUSE passthrough, so reads and
  writes of edited files run at disk speed.

### Explicit changesets

Edits leave the machine only through the commands that upload today:

- `gs create` and `gs modify` read the edits from the local index, not by
  walking the tree, so they cost the number of edits and never trigger lazy
  fetches. A file entry is hashed (with a size and mtime cache, as the stat cache
  does today) and uploaded with `UploadBlobs`; a reference needs no upload
  because the server has its content; a whiteout becomes a delete. The upload is
  the snapshot at that moment; later edits stay local until the next
  `gs modify`.
- `gs status` and `gs diff` read the same index and say which edits are local
  only and which are in the active changeset's current patchset.
- `gs submit` lands what is already uploaded; it sends no workspace content.
- Pre-upload (design/18) is not used in a mount. With edits indexed, capture is
  already proportional to the edits, which was the reason for pre-upload.

After a changeset lands and `gs sync` moves the base past it, local entries
whose content now matches the base are dropped, so the local layer shrinks back
to what is still unsubmitted.

### Moving the base

`gs sync` fetches the new head's manifest (one `Checkout` listing) and merges
only what both sides touched:

1. Paths with no local entry simply take the new base. No content is fetched,
   so a sync after a busy day costs one listing.
2. Paths with a local entry that upstream also changed go through the merge that
   eager workspaces use today (line merge by default, `--merge
   manual|ours|theirs`), fetching the three versions. Results go to the local
   layer; conflicts are recorded in `.gs/conflicts.json` as today.
3. The CLI writes the new manifest to the workspace's state and sends the
   daemon one `rebase` request over `control.sock` with its decision for every
   path both sides changed, each tagged with the local change it was decided
   for. The daemon holds the layer's lock (file system requests wait), rebases
   (`lazyfs.FS.Rebase`), journals the new layer as one snapshot record, points
   its fetcher at the new commit, rewrites `mount.json`, and tells the kernel to
   drop cached entries and pages for the paths that changed. A path that
   changed again after the CLI decided fails the whole request, which changes
   nothing; the command says to run it again.

A crash between the journal and `mount.json` is finished on the next mount:
the journal names its base commit, and that commit's manifest is on disk.

Switching changesets (`gs switch`, `up`, `down`, `restack`, `move`,
`detach`) sends a `switch` request instead: the target changeset's files become
reference entries (`lazyfs.FS.Switch`), so switching fetches nothing but
symlink targets; the sizes of files neither the cache nor the base knows come
from one `GetBlobStatus` call. As in eager workspaces, switching with
uncaptured local edits is refused, and untracked files that `.gitignore`
covers stay. The request carries the local changes the CLI saw, and the daemon
refuses it if they changed meanwhile, so a write is never dropped. In a
dependency tree, `gs sync` restacks on the server and then switches to the
restacked changeset over the new base, in one request.

A reference's content is fetched by path when the base has it there, from
another path when the base has it elsewhere (a renamed file), and by its hash
(`ReadBlobStream`) otherwise: a changeset's file, or a merge the server made.

After a change lands outside a dependency tree (`gs cs submit`), the workspace
moves onto the landed commit the same way: local changes the changeset carried
leave the local layer (the base has them now, merged with whatever else
landed), and later ones stay. In a tree, the next `gs sync` does it.

### Ignored files and build output

Build tools write into the mount (`node_modules`, `bin/`, caches). Those writes
go to the local layer like any other, and the ignore rules that apply today
(`.gitignore` files, for paths the base does not track) keep them out of edits.
`gs mount status` reports the local layer's size, and `gs restore --ignored`
removes ignored files.

### Agents

The agent daemon's conversation workspaces can be lazy too, which matters most
for agents in fresh sandboxes. Decided (2026-10-16): a mounted conversation
workspace still captures at the end of every turn, exactly as an eager one does.
That capture is a command the daemon runs on its owner's behalf, the same as a
person running `gs modify`; the mount itself still never uploads, and nothing
is sent while the turn runs. In a mount the capture reads the local index, so it
costs the turn's edits rather than a walk of the workspace.

`gs agent start --lazy` mounts each new conversation's workspace at its
directory (`gs mount --agent-conversation <id>`), one mount per conversation.
Where FUSE is missing it says so and checks out instead. On start, the daemon
mounts again the conversation workspaces that are not mounted (after a reboot),
so their files and metadata are there; closing a conversation unmounts its
workspace, then deletes it, or archives it with its state (re-attach with
`gs mount --workspace <id>`).

### Platforms

- **Linux:** FUSE 3 through `github.com/hanwen/go-fuse/v2`, unprivileged with
  `fusermount3`. Local layer files are offered to the kernel for passthrough
  (reads and writes at disk speed); go-fuse uses it where the kernel allows it
  (Linux 6.9 and later, and a mount with `CAP_SYS_ADMIN`) and serves them
  itself otherwise. Base content is never passed through: its file is in the
  shared cache, and the kernel keeps one backing file per inode, so a later
  write would land there.
- **macOS:** the same library over macFUSE (go-fuse does not speak FUSE-T),
  mounted with `noappledouble` and `noapplexattr` so Finder's `._*` and
  `.DS_Store` files never become edits. The mount table is read with
  `getfsstat`, and the control socket lives under `/tmp` (macOS's `$TMPDIR`
  would take half of the 104 bytes a socket path may have).
- **No FUSE** (containers without `/dev/fuse`, Windows): `gs mount` fails with a
  message that says so and suggests `gs init`; `gs agent start --lazy` checks
  out instead.

### Security

- The mount is visible to its user only (no `allow_other`).
- Content from the server is checked against its hash before it is cached or
  served.
- `local/` and the cache are `0700`; files keep their modes.
- The daemon reads the CLI's saved credential when it starts and again when the
  file changes, so `gs auth use-key` takes effect without a remount. A revoked
  key turns reads of uncached content into `EACCES`, reported by `gs mount
  status`.

## Code structure

- `internal/lazyfs`: the two-layer model with no FUSE in it: manifest, local
  index and journal, copy-up, references, whiteouts, edits, rebase onto a new
  manifest. All the semantics are tested here.
- `internal/lazyfs/fusefs`: a thin adapter from go-fuse callbacks to `lazyfs`.
- `internal/cli`: the edit source behind an interface, so `status`, `diff`,
  `create`, `modify` and `sync` read edits from a walk (eager workspaces) or
  from the local index (mounts) with the same code above it. `mount`,
  `unmount`, `fetch` and `restore` commands; `mount_layers.go` decides how
  sync, submit and the stack commands move a mount, and the mount daemon
  (`mount_serve_fuse.go`) applies it.

## Testing

- `lazyfs` against a reference model: random sequences of create, write,
  truncate, rename (files and directories), unlink, rmdir, mkdir and chmod,
  applied both to a `lazyfs` view and to a plain directory, compared after each
  step, including the edits each would produce and a rebase onto a changed base.
- Crash recovery: kill the daemon at every journal step and check that mounting
  again shows the same tree and the same edits.
- The read-only client: any write RPC from the daemon fails the test.
- End to end on Linux when `/dev/fuse` is available (skipped otherwise): mount,
  build `gitslice/gitslice` with `go build ./...`, edit, `gs create`, submit,
  `gs sync` across an upstream change to the same file, unmount, remount.
- Benchmarks (design/28 runner): time to mount, first read, `gs status` with ten
  edits in a 100,000-file slice, `go build` cold and warm, sync after 1,000
  upstream changes.

Targets on the design/28 machine against gitslice.io (about 200 ms away):

| Step | Target |
| --- | --- |
| `gs mount gitslice/gitslice`, cold | under 1 s |
| First read of an uncached small file | about one round trip |
| `gs status` / `gs create` with 10 edits, any slice size | under 100 ms plus upload |
| `gs sync` with no overlapping edits | one listing |
| `go build ./...` from cold | within 2x of `gs init` then build |

## Phases

1. `internal/lazyfs` and the edit-source interface, with eager workspaces moved
   onto it. No FUSE yet; this is where the semantics get settled.
2. Read-only `gs mount` on Linux: manifest, lazy reads, prefetch, `gs fetch`,
   `gs mount status`, `gs mount list`, `gs mount --workspace`.
3. Writable local layer, edits from the index, `gs create`/`modify` from it,
   `gs restore`, `gs mount discard`, crash recovery.
4. `gs sync` and stack switching in mounts; macOS; passthrough; prefetch tuning;
   agent daemon support. Done; macOS awaits a run on a Mac with macFUSE.

## Alternatives considered

- **Keep eager checkout.** Good up to a few thousand files (2.7 s for 779); it
  grows with the slice and always downloads everything.
- **Sparse eager checkout** (`gs init --only <path>`). No FUSE needed, and worth
  doing anyway, but the user has to know in advance what they will read.
- **Read-only FUSE base layer under the kernel's overlayfs.** Native write speed
  and no write path in our daemon, but Linux only and needs user namespaces;
  passthrough gets most of the speed.
- **An NFS loopback server** (what FUSE-T does). Portable without a kernel
  module, but slower and harder to invalidate precisely; kept as the macOS
  fallback.
- **Pre-uploading edits in the background.** Faster `gs create` for very large
  edits, but it would send content the user has not chosen to share, which is
  exactly what this design rules out.

## Open questions

1. ~~Should a mounted agent conversation workspace still capture at the end of
   every turn?~~ Yes; see "Agents".
2. ~~Default prefetch?~~ Small files beside the one read, in the same request
   (the phase 2 measurements: 28 round trips for a cold build of `gs`), plus
   `--prefetch <glob>` for what a user knows they will read.
3. Should ignored build output stay in the local layer, or go to a scratch
   directory outside the mount by default?
4. A size limit or warning for the local layer?
5. ~~macOS: macFUSE or FUSE-T?~~ macFUSE, the one go-fuse supports. FUSE-T
   would need a second adapter.
6. Once stable, should `gs init` mount by default where FUSE is available?
7. When to build one mount for many workspaces ("Multiple workspaces"): before
   the agent daemon uses lazy workspaces, or only if per-conversation mounts
   prove too heavy?

## Measurements (phase 2)

`gs mount gitslice/gitslice` (1,068 files) against gitslice.io from the
design/28 machine (about 200 ms away), with an empty client cache:

| Step | Time |
| --- | --- |
| Mount: listing, daemon start, kernel mount | 1.96 s |
| First read of an uncached file | 1.1 s (one round trip, with 15 small files beside it) |
| Another file in the same directory | 2 ms (prefetched) |
| `go build ./cmd/gs`, cold | opened 856 files; 28 round trips fetched 385 files (3.2 MiB) |
| `go build ./cmd/gs`, warm, from the mount / from a checkout | 0.64 s / 0.69 s |
| `grep -r` over the tree, warm, from the mount / from a checkout | 0.14 s / 0.05 s |

The prefetch of small files beside the one read (the default proposed in open
question 2) keeps a cold build to a few dozen round trips. Once content is
cached, builds run as fast as from a checkout; walks of the whole tree pay
FUSE's per-entry cost, about 3x on this kernel (5.15, without passthrough).
