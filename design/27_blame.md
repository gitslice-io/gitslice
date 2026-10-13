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

1. Read the file at the head.
2. Walk its history newest first, the same commits `gs log -- <path>` lists
   (following moves through file identity).
3. For each commit, read the file in its first parent. If it is not at the
   same path, the commit moved it: among the commit's changed paths, the one
   that had a file in the parent and none after, sharing the most lines,
   is where it came from.
4. Match the commit's version against the parent's line by line
   (`diffutil.MatchLines`: Myers' algorithm after trimming the common prefix
   and suffix, bounded at 4000 edits per step, beyond which a region counts as
   rewritten). Lines with no match are the commit's; the rest carry on to the
   parent's version.
5. A commit whose parent has no such file created it: every line still
   unaccounted for is its.

The walk stops at 500 commits; lines still unaccounted for are credited to the
oldest commit read and `truncated` is set. Each step reads one file version,
so the cost grows with the file's history, not the repository's size.

Not yet: blame at an older commit, ignoring whitespace-only changes, and a
derived-data cache (blame at a commit from blame at its parent), which would
make repeated blames of busy files cheaper.

## CLI

```bash
gs blame <path>                 # commit, changeset, author, date, line
gs blame <path> --lines 10,20   # just those lines
gs blame <path> --json          # ranges and commits
```

Paths are workspace-relative inside a workspace, or account-rooted
(`/account/...`). Inside a workspace the read goes through its slice;
`--slice` picks another.
