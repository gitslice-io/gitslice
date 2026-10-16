# 30. Lazy Workspaces: `gs mount`

Status: proposed (2026-10-16).

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
gs mount gitslice/gitslice ~/src/gitslice     # about 1 s; no file content yet
cd ~/src/gitslice
cat gitslice/gitslice/README.md               # fetched on first read, then cached
$EDITOR gitslice/gitslice/service/slice.go    # edit stays in the local layer
gs status                                     # lists local edits; reads nothing it does not need
gs diff                                       # local only
gs create --message "service: ..."            # now, and only now, content is uploaded
gs sync                                       # moves the base; untouched files cost nothing
gs unmount ~/src/gitslice                     # edits survive; gs mount picks them up again
```

- `gs mount <slice> <dir> [--foreground] [--prefetch <glob>...]` mounts a lazy
  workspace at an empty directory. Mounting the same directory again re-attaches
  the same workspace (found by the mount path in its `mount.json`), edits
  included.
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
  manifest         base layer listing for state.base_commit_id
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
gs mount gitslice/gitslice ~/w/fix-auth      # workspace 1
gs mount gitslice/gitslice ~/w/readme-page   # workspace 2, same slice
gs mount acme/payments    ~/w/payments       # workspace 3, another slice
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

Each mount runs its own daemon process. A crash or a hung fetch affects one
workspace, and the processes are small: the content they serve is on disk in
the shared cache, and fetches of the same content by two mounts at once are rare
enough not to coordinate.

**Later: one mount, many workspaces.** One mount could hold many workspaces as
directories (`~/gs/<name>/...`), with `gs workspace new <name> <slice>` creating
one instantly and no new mount. That suits the agent daemon, which makes a
workspace per conversation and would otherwise make a mount per conversation,
and platforms where mounts are heavy or scarce (macOS, containers that allow
one FUSE mount). It is not in the phases below; see the open questions.

### The mount daemon

`gs mount` starts one daemon per mount (detached unless `--foreground`). It
serves file system requests and nothing else:

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
  waits for its content; after 60 s, or offline, it fails with `EIO` and
  `gs mount status` says which path and why.
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

`gs sync` fetches the new head's manifest and merges only what both sides
touched:

1. Paths with no local entry simply take the new base. No content is fetched,
   so a sync after a busy day costs one listing.
2. Paths with a local entry that upstream also changed go through the merge that
   eager workspaces use today (line merge by default, `--merge
   manual|ours|theirs`), fetching the three versions. Results go to the local
   layer; conflicts are recorded in `.gs/conflicts.json` as today.
3. The CLI asks the daemon over `control.sock` to pause file system requests,
   swaps the manifest and the local index atomically, tells the kernel to drop
   cached entries and pages for the paths that changed, and resumes. Without a
   running daemon, the CLI swaps the files directly.

Switching changesets (`gs switch`, `up`, `down`) works the same way: the target
changeset's patchset becomes reference entries, so switching fetches nothing.
As in eager workspaces, switching with unsubmitted local edits is refused.

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

### Platforms

- **Linux:** FUSE 3 through `github.com/hanwen/go-fuse/v2`, unprivileged with
  `fusermount3`. Passthrough needs Linux 6.9.
- **macOS:** the same library over macFUSE, or FUSE-T (an NFS loopback server
  with no kernel extension). Phase 4.
- **No FUSE** (containers without `/dev/fuse`, Windows): `gs mount` fails with a
  message that says so and suggests `gs init`. `gs init --lazy=auto` mounts when
  it can and checks out eagerly when it cannot.

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
- `internal/lazyfs/fuse`: a thin adapter from go-fuse callbacks to `lazyfs`.
- `internal/cli`: the edit source behind an interface, so `status`, `diff`,
  `create`, `modify` and `sync` read edits from a walk (eager workspaces) or
  from the local index (mounts) with the same code above it. `mount`,
  `unmount`, `fetch` and `restore` commands.

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
   agent daemon support.

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
2. Default prefetch: small siblings on first read (proposed), whole directories
   on `readdir`, or nothing until asked?
3. Should ignored build output stay in the local layer, or go to a scratch
   directory outside the mount by default?
4. A size limit or warning for the local layer?
5. macOS: macFUSE (kernel extension, faster) or FUSE-T (no extension, NFS)?
6. Once stable, should `gs init` mount by default where FUSE is available?
7. When to build one mount for many workspaces ("Multiple workspaces"): before
   the agent daemon uses lazy workspaces, or only if per-conversation mounts
   prove too heavy?
