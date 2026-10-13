# Blame

Agents (and people) ask "who changed this line, in which change, and why?"
when debugging, reverting or reviewing. `BlameFile` answers it through the
API, without a checkout, as path history (`ListCommits` with a path) already
does for whole files.

## API

`RepositoryService.BlameFile(path, slice?, ref_name?)` returns, for the file
at the head of the target ref:

- `commit_id`, `path`, `line_count`;
- `ranges`: runs of lines (1-based `start_line`, `line_count`) and the commit
  that last changed them, covering every line in order;
- `commits`: each commit named, with author, message, time and the changeset
  that landed it (`changeset_id`), so a line leads to its review;
- `truncated`: the walk stopped before the file's first commit.

Authorization is ReadFile's: through the slice when one is given (the file
must be in it), else the path read rule (account members, or a signed-in
reader of a slice that includes it). Binary files are refused, and files over
4 MiB are too large.

## How it is computed

A file's blame at the commit that last changed it is its parent version's
blame carried through one diff: lines the diff matches keep their owner, the
rest belong to the commit. Blames are stored as **snapshots** (the
`blame_snapshots` table: commit, path, gzipped JSON of the version's content
hash and its owners as runs of lines), so a blame only walks back as far as the
newest version with a snapshot. It is derived data, as Scion calls it: computed
on demand, stored, and safe to delete.

1. Read the head's file entry (no content yet).
2. List the file's history newest first, the same commits `gs log -- <path>`
   lists (following moves through file identity). History is indexed shortly
   after a commit lands, so when the head's file is not the version the newest
   listed commit left, walk the main line back from the head for the commits
   that changed the file and are not listed yet (at most 100 steps).
3. Walk back commit by commit, reading the file in the commits' parents up to
   16 at a time ahead of the walk (not past a commit with a snapshot, where
   the walk likely stops; a move restarts the reads ahead for the old path).
   Stop at a commit with a snapshot of exactly
   the version being tracked (same content hash), or at the commit that
   created the file (no file in its parent). Otherwise read the file in the
   commit's first parent; if it is not at the same path, the commit moved it,
   and among the commit's changed paths the one that had a file in the parent
   and none after, sharing the most lines, is where it came from.
4. Carry the blame forward from that snapshot (or from the creation, which
   owns every line) through each newer version: match the two versions line by
   line (`diffutil.MatchLines`: Myers' algorithm after trimming the common
   prefix and suffix, bounded at 4000 edits per step, beyond which a region
   counts as rewritten). Matched lines keep their owner; the rest are the
   newer commit's.
5. Store the result as the snapshot of the newest version, unless history
   listing had not caught up with the head.

The walk stops at 500 commits; what is older is credited to the oldest commit
read, and `truncated` is set (and kept by snapshots built on it).

Cost: the first blame of a file reads one version per commit back to its
creation, 16 at a time. After that, a blame of an unchanged file reads no
file content at all, and a blame after k new changes reads about k versions.
Tree nodes are kept in the server's object cache (they were not, before:
the JSON decoder stopped short of the EOF the cache waits for), so the
directories a path shares with earlier versions are read once. Measured with
30 ms of object-storage latency and a cold cache, on a 3,000-line file with
60 versions:

| Blame | Before snapshots | Snapshots | Snapshots, parallel reads, cached trees |
|---|---|---|---|
| First | 7.6 s | 7.6 s | 0.95 s |
| Again | 7.6 s | 56 ms | 57 ms |
| After one more change | 7.6 s | 233 ms | 213 ms |

Not yet: blame at an older commit, and ignoring whitespace-only changes.

## CLI

```bash
gs blame <path>                 # commit, changeset, author, date, line
gs blame <path> --lines 10,20   # just those lines
gs blame <path> --json          # ranges and commits
```

Paths are workspace-relative inside a workspace, or account-rooted
(`/account/...`). Inside a workspace the read goes through its slice;
`--slice` picks another.
