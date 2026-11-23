# Dependent Changesets Design

This document defines Gitslice's dependent-changeset workflow: splitting a
larger change into multiple reviewable changesets, basing one changeset on
another, updating descendants when a base changeset changes, and submitting in
base-before-dependent order.

In product UX (CLI, web UI, and user-facing docs), Gitslice avoids exposing
"stack" as a first-class concept: users work with **changesets**, **base
changesets**, **dependent changesets**, and **dependency trees**. Under the
hood, the implemented gRPC API (`ChangesetStackService` in
`proto/core/v1/changeset.proto`) and PostgreSQL storage retain `stack_*` /
`parent_*` names; renaming the wire API and tables is deferred (§4, §6, §10).

Related documents:

- [04_cli_design.md](04_cli_design.md): native CLI command tree
- [07_conflict_resolution.md](07_conflict_resolution.md): restack three-way merge (§7.3) and `.gitslice/merge.yaml` (§7.5)

## 1. Goals

- Allow a changeset to be based on either a commit or another changeset's
  current patchset.
- Let users create, inspect, move, update, and submit dependent changesets
  without managing a named stack object.
- Keep each changeset as the review unit with its own patchsets, approvals,
  checks, status, and submit result.
- Preserve single-slice behavior: a dependent changeset stays in the same
  authoring slice and target ref as its base changeset.
- Make dependency state durable enough for server-side validation, preview
  trees, conflict detection, and ordered submit.

## 2. Non-Goals

- Cross-slice dependent changesets.
- Treating patchsets as review objects (a patchset remains a revision of one
  changeset).
- Exposing a separate "stack" tab, route, or CLI namespace.
- Git branch compatibility for dependent changesets.

## 3. Product Vocabulary

Use these terms in UI, CLI output, and user-facing docs:

- `changeset`: the review object.
- `base commit`: the accepted commit a root changeset is based on.
- `base changeset`: the changeset a dependent changeset is based on.
- `base patchset`: the exact base patchset used when the dependent patchset was
  created.
- `dependent changeset`: a changeset whose base is another changeset.
- `dependency tree`: the derived tree of base and dependent changesets.
- `update dependents`: create new patchsets for descendants whose recorded base
  patchset is stale.
- `submit with dependencies`: submit a selected changeset and required
  unsubmitted ancestors in dependency order.

## 4. Core Model (Implemented)

The durable model is a same-slice tree of changesets stored with `stack_*` /
`parent_*` fields on `Changeset` and `Patchset` (`proto/core/v1/changeset.proto`):

```text
Changeset:
  id
  authoring_slice
  target_ref
  base_commit_id
  base_kind                    # "commit" or "patchset"
  stack_id                     # internal dependency-tree grouping id
  stack_order                  # display order within the tree
  parent_changeset_id          # base changeset id (empty for root)
  parent_patchset_id           # base patchset id recorded at creation/update
  stack_depth                  # depth from root (0 for root)
  sibling_order                # ordering among siblings under the same parent
```

Patchsets record the actual materialization base:

```text
Patchset:
  changeset_id
  base_commit_id
  base_kind                    # "commit" or "patchset"
  base_patchset_id             # base patchset id when base_kind is "patchset"
  base_tree_id                 # tree read by validation and diff preview
  result_tree_id               # materialized result tree
  stack_parent_patchset_id     # parent patchset id recorded for stack restack
```

For a root changeset, `base_kind = "commit"` and `base_tree_id` is the root
tree for `base_commit_id`. For a dependent changeset, `base_kind = "patchset"`,
`parent_patchset_id` / `base_patchset_id` points at the base changeset's
current patchset at creation/restack time, and `base_tree_id` is that base
patchset's `result_tree_id`.

## 5. Validation Rules

Creation and mutation enforce:

1. Base and dependent changesets are in the same authoring slice and target ref.
2. A dependent changeset's base patchset must match the base changeset's current
   patchset unless explicitly restacking stale descendants.
3. No cycles.
4. Submitted changesets are immutable anchors: dependents can update onto a
   submitted base changeset's result, but a submitted changeset cannot be moved.
5. A changeset with non-terminal dependents cannot be abandoned unless
   descendants are moved, updated, or abandoned.
6. Submit rejects a dependent changeset (`blocked_on_parent` /
   `BlockedOnBaseChangeset`) when required unsubmitted ancestors are not
   submitted first, or when `base_patchset_id` is stale relative to its base
   changeset's current patchset.

## 6. Implemented gRPC API (`ChangesetStackService`)

While CLI and web UX present dependency-oriented vocabulary, the implemented
wire API lives on `ChangesetStackService` alongside `ChangesetService` in
`proto/core/v1/changeset.proto`:

```protobuf
service ChangesetStackService {
  rpc CreateStack(CreateStackRequest) returns (ChangesetStack);
  rpc GetStack(GetStackRequest) returns (ChangesetStack);
  rpc ListStacks(ListStacksRequest) returns (ListStacksResponse);
  rpc AddStackEntry(AddStackEntryRequest) returns (Changeset);
  rpc MoveStackEntry(MoveStackEntryRequest) returns (ChangesetStack);
  rpc ReparentStackEntry(ReparentStackEntryRequest) returns (ChangesetStack);
  rpc DetachStackEntry(DetachStackEntryRequest) returns (DetachStackEntryResponse);
  rpc Restack(RestackRequest) returns (RestackResponse);
  rpc SubmitStack(SubmitStackRequest) returns (SubmitStackResponse);
}
```

Key implemented messages:

```protobuf
message CreateChangesetRequest {
  SliceRef authoring_slice = 1;
  string target_ref = 2;
  string base_commit_id = 3;
  string title = 4;
  string description = 5;
  string stack_id = 6;
  string parent_changeset_id = 7;
  string parent_patchset_id = 8;
}

message ChangesetStack {
  string id = 1;
  SliceRef authoring_slice = 2;
  string target_ref = 3;
  string base_commit_id = 4;
  string title = 5;
  string status = 6;
  string active_entry_id = 7;
  string root_entry_id = 8;
  repeated ChangesetStackEntry entries = 9;
  string created_by = 10;
  string created_at = 11;
  string updated_at = 12;
}

message ChangesetStackEntry {
  string stack_id = 1;
  string changeset_id = 2;
  string parent_changeset_id = 3;
  string parent_patchset_id = 4;
  int64 sibling_order = 5;
  int64 display_order = 6;
  int64 depth = 7;
  string state = 8;
  Changeset changeset = 9;
}

message RestackRequest {
  string stack_id = 1;
  string start_changeset_id = 2;
  bool include_siblings = 3;
  string target_base_commit_id = 4;
}

message SubmitStackRequest {
  string stack_id = 1;
  string subtree_root_changeset_id = 2;
}
```

**Deferred API rename**: folding `ChangesetStackService` into `ChangesetService`
under dependency-oriented RPC and field names (`GetDependencyTree`,
`UpdateDependents`, `SubmitWithDependencies`, `base_changeset_id`,
`dependency_depth`, etc.) is deferred. Until then, CLI and web clients
translate `ChangesetStackService` responses into dependency-tree terminology at
the presentation layer.

## 7. Diff And Preview Semantics

Diff base is a view over the stored patchset base. Users may compare arbitrary
patchsets in the diff viewer, but changing a changeset's base is a data
mutation that creates a new patchset or updates dependency metadata.

Rules:

- `DiffChangeset` defaults to the selected patchset against its recorded base.
- Comparing two patchsets within one changeset remains supported.
- The changeset detail UI exposes all patchsets through a horizontal timeline
  with patchset dots and draggable `From`/`To` handles (`From = Recorded base`
  compares against the stored materialization base).
- Selecting a different diff base is review state only (`DiffChangeset` with
  `from_patchset` and `to_patchset`); it never mutates base metadata.
- Updating a base changeset triggers restack (`ChangesetStackService.Restack`),
  creating new dependent patchsets derived from the new base patchset's
  `result_tree_id` using three-way line merge and `.gitslice/merge.yaml` rules
  (see [07_conflict_resolution.md](07_conflict_resolution.md#73-restacking-a-dependency-tree)).
- Preview trees and submit validation always use the stored patchset base.

## 8. CLI Design

The top-level workflow (`internal/cli/commands_stack.go`) is changeset-first
and uses dependency-oriented vocabulary:

```text
gs create -m <title> [--description <text> | --description-file <path>] [--base <changeset>] [--root] [--sibling] [--all] [--submit] [--watch-timeout 15m] [--if-changed | --allow-empty] [--yes]
gs cs edit [changeset] [--title <title>] [--description <text> | --description-file <path>]
gs modify [--all] [-m <title>] [--no-update-dependents] [--submit] [--watch-timeout 15m]
gs submit [changeset] [--with-dependencies] [--subtree <changeset>] [--no-watch] [--watch-timeout 15m]
gs deps [dependency-tree]
gs update-dependents [changeset] [--children|--all]
gs switch <changeset>
gs up [changeset]
gs down [steps]
gs top
gs bottom
gs move <changeset> --onto <base|root> [--order <n>]
gs insert --base <changeset> --message <title>
gs detach <changeset> [--message <title>]
```

Key behavior:

- `gs create --submit` and `gs modify --submit` wait for un-reported required
  checks (up to `--watch-timeout`, default `15m`), submit, and wait for
  publish.
- `gs modify` automatically updates dependent changesets via `Restack` unless
  `--no-update-dependents` is passed.
- `gs move <cs> --onto <base|root> [--order <n>]` reparents a changeset (and
  optionally sets its sibling order under the new base).
- `gs sync` in a workspace with a dependency tree restacks the tree onto the
  latest `refs/global/main` head.
- Text output says `dependency tree`, `base changeset`, `dependent changeset`,
  and `needs update`, displaying 10-char `ShortChangesetID` handles.

## 9. Web Design

The web UI has no separate Stacks or Dependencies tab; the Changesets area owns
base/dependent review:

- `/changesets` lists changesets for a slice.
- The changeset detail page shows a `Base changeset` link when based on another
  changeset, keeps the title to one line, and provides the patchset timeline
  for `Recorded base -> patchset` or patchset-to-patchset diffs.
- Mobile review prioritizes the changed-file tree with toggleable diff bodies.
- Actions are labelled `Create dependent changeset`, `Update dependents`,
  `Move to new base`, and `Submit with dependencies`.

## 10. Storage

Physical PostgreSQL storage uses:

```text
changeset_stacks
changeset_stack_entries
changesets.stack_id
changesets.stack_order
changesets.parent_changeset_id
changesets.parent_patchset_id
changesets.stack_depth
changesets.sibling_order
patchsets.base_patchset_id
patchsets.stack_parent_patchset_id
```

These remain internal implementation details; any future physical rename to
dependency-oriented table/column names is deferred until the gRPC API rename is
scheduled.

## 11. Title And Description

A changeset has a one-line title and a Markdown description, matching a commit
subject and body. `NormalizeChangesetText` (`internal/storage`) enforces this
on every create path: a multi-line title is split at the first line (subject up
to 200 characters), with the remainder prepended to the description. The commit
a changeset lands as uses `title\n\ndescription` as its message. Both fields
can be edited only while the changeset is open (`UpdateChangesetInfo`;
`gs cs edit`; `-m` on `gs modify`; the Edit button in the web UI).
