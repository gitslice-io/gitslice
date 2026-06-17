# Stacked Changesets Design

This document describes how Gitslice can support stacked changesets inside one
workspace while preserving the current native storage and submit model.

Related documents:

- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md):
  top-level workspace, changeset, patchset, and submit model
- [02_storage.md](02_storage.md): refs, commits, patchsets, path heads, and
  metadata schema
- [03_core_api.md](03_core_api.md): gRPC API boundary
- [04_cli_design.md](04_cli_design.md): native CLI workspace behavior
- [07_conflict_resolution.md](07_conflict_resolution.md): path predicates,
  path-head admission, sync, and batched publish
- [11_web_interface_design.md](11_web_interface_design.md): current web
  inspection surface

## 1. Goal

Allow a user to work on multiple dependent changesets in the same local
workspace, including sibling branches under the same parent, for example:

```text
refs/global/main at A

acme/payment@42: introduce payment parser
  acme/payment@43: use parser in payment API
    acme/payment@45: update tests for API behavior
  acme/payment@44: expose parser metrics
```

The workspace materialized tree for a selected leaf follows only that entry's
ancestor path. For `@45`, the workspace tree is:

```text
A + @42 current patchset + @43 current patchset + local edits for @45
```

Sibling entries such as `@44` are not included in the working tree while `@45`
is active. Switching to `@44` rematerializes the workspace through `@42` and then
`@44`.

Each stack entry remains a normal changeset with its own patchsets, review,
approval state, check state, submit status, audit trail, and submitted commit.

## 2. Non-Goals

Stacked changesets must not introduce:

- cross-slice changesets
- multi-slice workspaces
- atomic multi-changeset submit
- direct native commit creation from the CLI
- Git branches as the internal source of truth
- a Git-style staging area

A stack is a same-slice, same-target-ref rooted changeset tree for review, local
workspace materialization, restacking, and ordered submit. It is not a new
submission atomicity boundary.

The MVP supports a tree, not a general DAG. Every non-root entry has exactly one
parent. A parent may have multiple children. Cycles and multiple-parent merges
are rejected.

This intentionally narrows the older "no server-side relationship that links
multiple changesets" rule. The revised rule is:

```text
Same-slice changesets may have dependency links for stacked review and replay,
but the server must not treat those links as atomic multi-changeset submission.
```

## 3. Core Model

Use a new `ChangesetStack` object plus per-changeset parent links. Do not model
stack entries as patchsets. A patchset is a revision of one review object; a
stack entry is a separate review object.

```text
ChangesetStack:
  id
  authoring_slice
  target_ref
  base_commit_id
  title
  status
  active_entry_id
  root_entry_id
  entries[]
  created_by
  created_at
  updated_at

ChangesetStackEntry:
  stack_id
  changeset_id
  parent_changeset_id
  parent_patchset_id
  sibling_order
  display_order
  depth
  state
```

The root entry has no parent. Every non-root entry depends on one parent
changeset's current patchset at the time the child patchset is created or
restacked. Multiple children may depend on the same parent patchset. The tree is
ordered for display by `(parent_changeset_id, sibling_order)` and can be rendered
as a preorder traversal, but correctness uses parent links rather than global
position numbers.

Patchsets need explicit base-source metadata:

```text
Patchset:
  base_kind                    # commit or patchset
  base_commit_id               # nearest accepted commit base
  base_patchset_id             # present when base_kind is patchset
  base_tree_id                 # tree snapshot used as old side for this patchset
  result_tree_id               # preview tree after applying file edits
  stack_parent_patchset_id     # same as base_patchset_id for stacked children
```

For the first entry, `base_kind = commit` and `base_tree_id` is the root tree for
`base_commit_id`. For a child entry, `base_kind = patchset` and `base_tree_id` is
the parent's `result_tree_id`.

The changeset-level `base_commit_id` stays useful for audit and target-ref
lineage, but the patchset's `base_tree_id` is the old side for diff validation,
preview reads, and child stack materialization.

## 4. Server Changes

### 4.1 API Shape

Add stack-aware fields to existing changeset messages:

```proto
message Changeset {
  string stack_id = 19;
  int64 stack_order = 20;
  string parent_changeset_id = 21;
  string parent_patchset_id = 22;
  string base_kind = 23;
  int64 stack_depth = 24;
  int64 sibling_order = 25;
}

message Patchset {
  string base_kind = 17;
  string base_patchset_id = 18;
  string base_tree_id = 19;
  string result_tree_id = 20;
  string stack_parent_patchset_id = 21;
}

message CreateChangesetRequest {
  string stack_id = 6;
  string parent_changeset_id = 7;
  string parent_patchset_id = 8;
}

message UpdateChangesetRequest {
  string base_kind = 7;
  string base_patchset_id = 8;
  string expected_parent_patchset_id = 9;
}
```

Add a new stack service:

```proto
service ChangesetStackService {
  rpc CreateStack(CreateStackRequest) returns (ChangesetStack);
  rpc GetStack(GetStackRequest) returns (ChangesetStack);
  rpc ListStacks(ListStacksRequest) returns (ListStacksResponse);
  rpc AddStackEntry(AddStackEntryRequest) returns (Changeset);
  rpc MoveStackEntry(MoveStackEntryRequest) returns (ChangesetStack);
  rpc ReparentStackEntry(ReparentStackEntryRequest) returns (ChangesetStack);
  rpc Restack(RestackRequest) returns (RestackResponse);
  rpc SubmitStack(SubmitStackRequest) returns (SubmitStackResponse);
}
```

The existing `ChangesetService` remains valid. A stack-aware client can create
and update entries through the new service, while older clients can continue to
operate on individual changesets.

### 4.2 Storage

Add stack tables:

```text
changeset_stacks(
  id primary key,
  authoring_slice_id references slices(id),
  target_ref references refs(name),
  base_commit_id not null,
  title not null,
  status not null,
  active_entry_changeset_id references changesets(id),
  root_entry_changeset_id references changesets(id),
  created_by references subjects(id),
  created_at,
  updated_at
)

changeset_stack_entries(
  stack_id references changeset_stacks(id),
  changeset_id references changesets(id),
  parent_changeset_id references changesets(id),
  parent_patchset_id references patchsets(id),
  sibling_order bigint not null,
  display_order bigint not null,
  depth bigint not null,
  state text not null,
  created_at,
  updated_at,
  primary key(stack_id, changeset_id),
  unique(stack_id, parent_changeset_id, sibling_order),
  unique(stack_id, display_order)
)
```

Extend `changesets`:

```text
stack_id references changeset_stacks(id)
stack_order bigint
stack_depth bigint
sibling_order bigint
parent_changeset_id references changesets(id)
parent_patchset_id references patchsets(id)
```

Extend `patchsets`:

```text
base_kind text not null default 'commit'
base_patchset_id references patchsets(id)
base_tree_id text
result_tree_id text
stack_parent_patchset_id references patchsets(id)
```

`result_tree_id` is a tree preview, not a commit. It must be stored in the same
object store as normal immutable tree nodes. A stack child may reference that
preview as its base tree even before the parent changeset has submitted.

Useful indexes:

```text
changeset_stack_entries(stack_id, parent_changeset_id, sibling_order)
changeset_stack_entries(stack_id, display_order)
changeset_stack_entries(parent_changeset_id)
changeset_stack_entries(parent_patchset_id)
```

PostgreSQL should enforce at most one root per stack with a partial unique index
where `parent_changeset_id is null`. Recursive descendant lookups can use a
bounded recursive CTE for the MVP; a materialized ancestry table can be added
later if tree operations become hot.

### 4.3 Validation

Stack creation and entry mutation must enforce:

1. Every entry has the same `authoring_slice_id`.
2. Every entry has the same `target_ref`.
3. Every changed path remains inside the authoring slice.
4. A child entry's `parent_patchset_id` is the parent's current patchset unless
   the request explicitly says it is restacking from an older parent.
5. Exactly one root entry has no parent.
6. Every non-root entry has exactly one parent in the same stack.
7. A parent may have multiple children.
8. Cycles are impossible, and arbitrary DAG merges are rejected.
9. Moving or reparenting an entry must preserve slice, target-ref, and acyclic
   tree invariants.
10. A parent cannot be abandoned while active children depend on its current
   patchset unless the caller also abandons or detaches the descendants.
11. Submitted entries are immutable stack anchors. Restacking descendants may
   update their base to the submitted parent's commit, but must not rewrite the
   submitted parent.

Patchset validation changes from "read path bases from `base_commit_id` only" to
"read path bases from the patchset base snapshot." The base snapshot is:

```text
base_kind == commit:
  root tree of base_commit_id

base_kind == patchset:
  result_tree_id of base_patchset_id
```

Path base predicates still record enough information to validate submit against
accepted path-head state. For child stack entries, predicates are calculated
against the parent preview tree, then transformed at submit time through ordered
parent admission.

### 4.4 Preview Tree Creation

`UpdateChangeset` must create or reuse a patchset preview tree:

```text
1. Load base tree from commit root or parent patchset result.
2. Apply file edits to produce result tree.
3. Store any new tree nodes in object storage.
4. Store `base_tree_id` and `result_tree_id` on the patchset.
5. Store changed paths, read set, write set, path bases, coverage, conflicts,
   and submit requirement snapshot as today.
```

The preview tree is required for:

- child stack materialization
- server-side diff
- web inspection
- restack conflict detection
- future Git-compatible changeset refs

### 4.5 Submit

Submitting a single stack entry directly remains allowed, but the server must
reject a child entry when its unsubmitted parent has not been accepted:

```text
BlockedOnStackParent:
  parent changeset is draft, needs rebase, failed, abandoned, or not yet accepted
```

`SubmitStack` submits a selected subtree in topological preorder. When no
subtree root is provided, it starts at the stack root:

```text
walk selected subtree parent-before-child:
  if entry submitted:
    continue
  verify parent state
  submit current changeset through normal SubmitChangeset admission
  wait for admission, not necessarily publish
```

The existing path-head admission model remains the correctness boundary:

1. The first entry validates against current accepted path heads.
2. Admission updates `path_heads` to the accepted post-patch fingerprints.
3. Each child validates only after its parent has been accepted or submitted.
4. The publisher may batch the pending rows into one commit chain and one target
   ref CAS.

If entry `@43` fails, entry `@42` may still remain accepted or submitted. Sibling
subtrees that do not depend on `@43` may continue if their own parent chain is
valid and required checks pass. The server must report per-entry status instead
of rolling back already accepted parents.

### 4.6 Publish

The current publisher already supports ordered pending rows and commit chains.
For stacks, it should preserve the pending publish sequence created by admission:

```text
A -> commit(@42) -> commit(@43) -> commit(@45) -> commit(@44)
```

The exact order is the admission sequence, which must be a topological order for
the stack entries being submitted. Siblings can be ordered by `sibling_order`,
creation time, or explicit submit request order. If stack entries were admitted
consecutively and share a target ref, the existing batching path can publish them
in one target-ref CAS. If other compatible changesets are interleaved by
admission sequence, the publisher may include them as long as the sequence order
and path-head invariants remain valid.

### 4.7 Restack

Restack creates new patchsets for descendants whose parent patchset changed,
whose parent was reparented, or whose base commit moved.

```text
Restack input:
  stack_id
  start_changeset_id
  include_siblings false by default
  target_base_commit_id optional

For each descendant in parent-before-child order:
  load previous child file edits
  load new parent result tree
  replay child edits onto new parent result tree
  if clean, create normal patchset with new parent_patchset_id
  if conflicted, create sync/rebase patchset with PatchsetConflict records
```

By default, restack affects the selected entry's descendants, not its siblings.
If a parent patchset changes, all child subtrees depending on the old parent
patchset become `NeedsRestack`. The user may restack one child subtree at a time
or pass an explicit option to restack every stale child subtree.

Approvals may be forwarded only under the existing approval preservation rule:
same paths, same operations, same content hashes, and unchanged submit
requirements.

### 4.8 New States And Reasons

Add stable status or blocked-reason values:

```text
BlockedOnStackParent
NeedsRestack
StackParentAbandoned
StackParentPatchsetChanged
StackSubmitPartial
```

`StackSubmitPartial` belongs to stack-level reporting, not individual changeset
status. Individual changesets should keep normal states such as `draft`,
`pending_publish`, `submitted`, `needs_rebase`, or `failed`.

## 5. CLI Changes

### 5.1 Workspace State

Replace the single-current changeset shape with stack-aware state:

```json
{
  "base_commit_id": "sha256:...",
  "active_stack_id": "stk_...",
  "active_changeset_id": "cs_...",
  "active_patchset_id": "ps_...",
  "stack_entries": [
    {
      "changeset_id": "cs_...",
      "changeset_handle": "acme/payment@42",
      "patchset_id": "ps_...",
      "patchset_number": 1,
      "parent_changeset_id": "",
      "parent_patchset_id": "",
      "sibling_order": 1,
      "display_order": 1,
      "depth": 0
    }
  ]
}
```

The CLI should read older `.gs/state.json` files and migrate in memory:

```text
current_changeset_id -> active_changeset_id
current_patchset_id  -> active_patchset_id
no active_stack_id   -> single-entry implicit stack
```

The workspace still has exactly one bound slice. Stack state does not change
slice binding.

### 5.2 Commands

Add stack commands:

```bash
gs stack create --title <title>
gs stack list [--status <status>]
gs stack show [stack]
gs stack switch <changeset>
gs stack child [--parent <changeset>] --title <title>
gs stack move <changeset> --onto <parent|root>
gs stack insert --parent <changeset> --title <title>
gs stack restack [--from <changeset>] [--all-children]
gs stack submit [--from <changeset>] [--subtree] [--no-watch] [--watch-timeout <duration>]
gs stack detach <changeset>
```

Keep changeset commands usable:

```bash
gs cs create --title <title>          # create first stack entry when no stack exists
gs cs create --stack --title <title>  # create child of current stack entry
gs cs update                          # update active stack entry
gs cs submit [changeset]              # submit one entry, with parent checks
gs cs status [changeset]
gs cs diff [changeset]
```

The CLI should not make `gs cs create` silently create a child when an active
entry exists. Require `--stack` or `gs stack child` so users do not accidentally
split a patchset revision into a dependent changeset.

### 5.3 Create And Child Flow

First entry:

```text
1. Scan workspace edits.
2. Validate edits against the bound slice and current base commit.
3. CreateStack.
4. CreateChangeset for the first entry.
5. UpdateChangeset with `base_kind=commit`.
6. Store stack and active entry state.
```

Create child:

```text
1. Resolve the requested parent, defaulting to the active stack entry.
2. Require the parent to have a current patchset with `result_tree_id`.
3. Materialize workspace as the parent ancestor path plus local edits.
4. Scan local edits relative to that materialized tree.
5. CreateChangeset with `stack_id`, `parent_changeset_id`, and
   `parent_patchset_id`.
6. UpdateChangeset with `base_kind=patchset`.
7. Switch active entry to the new child.
```

Create sibling:

```text
1. Resolve the active entry's parent.
2. Materialize workspace as that parent ancestor path.
3. Create a new child under the same parent.
4. Assign `sibling_order` after the active entry unless explicitly requested.
```

Insert between parent and children:

```text
1. Create a new child under the selected parent.
2. Reparent selected existing children onto the new entry.
3. Restack the reparented subtrees onto the new entry's result tree.
```

Move or reparent:

```text
1. Resolve entry and new parent.
2. Reject if the new parent is inside the entry's own subtree.
3. Update parent link and sibling order.
4. Restack the moved entry and all recursive descendants.
```

### 5.4 Workspace Materialization

`gs status`, `gs diff`, `gs cs update`, and `gs sync` must calculate local edits
against the active entry's base snapshot, not only the target ref commit.

```text
active entry is root:
  old side = workspace base commit

active entry is child:
  old side = parent patchset result tree
```

The workspace files on disk represent the active entry's ancestor path plus local
edits. Switching entries must rewrite files to:

```text
base commit + ancestors(selected entry) + selected entry current patchset
```

When the selected entry has multiple children, commands that move "up" toward
children must require an explicit child selector or open an interactive picker.
Commands that move "down" toward the root are unambiguous because each entry has
at most one parent.

Before switching, the CLI must stop if uncommitted local edits are not captured
in the active entry's latest patchset, unless the user passes an explicit future
force or stash-like option. The MVP should avoid hidden local stash behavior.

### 5.5 Update Flow

`gs cs update` updates the active entry only.

For a parent entry update, every child subtree depending on the old parent
patchset becomes stale:

```text
@42 creates patchset 2
@43 parent_patchset_id still points to @42.1
@43 state becomes NeedsRestack
@44 parent_patchset_id still points to @42.1
@44 state becomes NeedsRestack
```

The CLI should show:

```text
updated changeset acme/payment@42 patchset 2
descendants need restack: acme/payment@43, acme/payment@44
```

### 5.6 Restack Flow

`gs stack restack` calls the server `Restack` RPC and then updates local
workspace state from the returned entries.

Text output should be per entry:

```text
restacked acme/payment@43 patchset 3
restacked acme/payment@45 patchset 2 with conflicts
```

If any restacked patchset has conflicts, the active workspace should switch to
the first conflicted entry and materialize the conflict state in `.gs/` plus the
working tree. `gs stack submit` must reject until conflicts are resolved and
`gs cs update` creates a normal patchset.

### 5.7 Submit Flow

`gs stack submit`:

```text
1. Load stack state.
2. Resolve the selected subtree; default is the whole stack.
3. Refresh every entry from the server.
4. Stop if any entry in the selected subtree needs restack or has unresolved
   conflicts.
5. Submit entries parent-before-child.
6. By default, wait for every entry to reach submitted or until timeout.
7. If some entries submit and a later entry fails, report partial progress.
8. Update workspace base to the latest published commit only after the active
   entry is submitted and visible.
```

Example output:

```text
submitted acme/payment@42 pending publish
submitted acme/payment@43 pending publish
blocked acme/payment@45: required check unit failed
submitted acme/payment@44 pending publish
stack partially submitted: 3 submitted, 1 blocked
```

### 5.8 Status And Diff Output

`gs status` should show both workspace and stack state:

```text
slice: acme/payment
base: sha256:abc123
stack: stk_123
active: acme/payment@43

stack:
  1 acme/payment@42 patchset 2 draft
  +- acme/payment@43 patchset 1 draft needs_update
* |  `- acme/payment@45 patchset 1 draft
  `- acme/payment@44 patchset 1 needs_restack

changed paths:
  /acme/payment/api.go
```

`gs diff` without arguments diffs local edits against the active entry base
snapshot. `gs cs diff @43` delegates to server-side changeset diff and uses the
patchset's own base snapshot.

### 5.9 JSON Compatibility

Machine-readable output should add fields without removing existing ones:

```json
{
  "changeset_id": "cs_...",
  "patchset_id": "ps_...",
  "stack_id": "stk_...",
  "stack_order": 3,
  "stack_depth": 2,
  "sibling_order": 1,
  "parent_changeset_id": "cs_...",
  "parent_patchset_id": "ps_...",
  "needs_restack": false
}
```

Older scripts that only read `changeset_id` and `patchset_id` should continue to
work for single-entry changesets.

## 6. Web Changes

The web UI should expose stacks as review and inspection structure once the
server APIs exist. Until then, it can only show stack fields embedded in
`GetChangeset`.

### 6.1 Navigation

Add a Stack section under the existing changeset area:

```text
Sidebar:
  Source
  Slices
  Changesets
  Stacks
```

Do not show stacks in slice detail until `ListStacks` or stack-filtered
`ListChangesets` exists.

### 6.2 Routes

Add routes:

```text
/stacks                               Stack lookup or list when API exists
/stacks/new                           Create stack
/stacks/{id}                          Stack detail
/stacks/{id}/restack                  Restack confirmation and result
/stacks/{id}/submit                   Stack submit progress
/changesets/{id}?stack={stack_id}     Changeset detail focused in stack context
```

The changeset detail page should link back to its stack when `stack_id` is
present on the loaded changeset.

### 6.3 Stack Detail Page

The stack detail page should show a compact tree of entries:

```text
acme/payment stack: payment parser rollout
target: refs/global/main   base: sha256:abc123

Entries
  acme/payment@42  draft            patchset 2
  +- acme/payment@43  needs_restack patchset 1
  |  `- acme/payment@45 blocked     patchset 1
  `- acme/payment@44  draft         patchset 1

[Restack] [Submit Stack]
```

For each entry, show:

- changeset handle
- title
- status
- current patchset number
- parent changeset handle
- parent patchset number
- child count
- tree depth
- changed path count
- submit blocked reason

Selecting an entry opens the normal changeset detail content in the page, scoped
to that entry.

### 6.4 Create Stack Page

The web create flow should mirror CLI explicitness:

```text
1. Select authoring slice.
2. Select target ref.
3. Enter first changeset title and description.
4. Add file edits.
5. Validate and upload blobs.
6. Create stack, first changeset, and first patchset.
```

Creating a child entry should be a separate action from "add patchset":

```text
[Add Patchset] revises current changeset
[Add Child] creates a dependent changeset
[Add Sibling] creates another child under the same parent
```

The UI must avoid presenting child creation as a patchset update.

Moving an entry to another parent should be a separate tree action. The UI must
preview the affected subtree and state that descendants will be restacked.

### 6.5 Restack UI

Restack should be a deliberate action with a preview:

```text
Restack from: acme/payment@43
Parent changed: acme/payment@42.1 -> acme/payment@42.2

Affected entries:
  acme/payment@43
    acme/payment@45

[Restack]
```

After the RPC returns, show per-entry results:

- clean restack with new patchset number
- conflict restack with conflict path count
- unchanged entry
- failed entry with server reason

If conflicts exist, link directly to the conflicted changeset detail and show
the server-provided `PatchsetConflict` fields. The web MVP does not need an
inline conflict editor.

### 6.6 Submit Stack UI

Stack submit should show progress by entry:

```text
Submitting stack
  acme/payment@42  accepted, pending publish
  +- acme/payment@43  accepted, pending publish
  |  `- acme/payment@45  blocked: required check unit failed
  `- acme/payment@44  accepted, pending publish
```

The UI must make partial submit visible. It should not imply rollback when a
later child fails after a parent or sibling was accepted.

### 6.7 Components

Add components:

```text
<StackLookupPage />
<CreateStackPage />
<StackDetailPage>
  <StackHeader />
  <StackEntryList />
  <StackEntryDetail />
  <StackActionBar />
  <StackMoveDialog />
  <RestackResultPanel />
  <StackSubmitProgress />
</StackDetailPage>
```

Reuse existing changeset components for entry detail:

- `ChangesetHeader`
- `PatchsetTabs`
- `FileEditTable`
- `CoverageTable`
- `PathBaseTable`
- `SubmitRequirementIds`

### 6.8 Web API Client

Add typed client methods for the stack RPCs through grpc-gateway. The browser
should continue using the same bearer token and auth path as changeset requests.

Polling rules:

- poll `GetStack` while `SubmitStack` has in-progress entries
- poll individual `GetChangeset` rows when showing detailed publish status
- stop polling when all entries are terminal, blocked, or waiting on user action

## 7. Lessons From Graphite

Graphite's public docs are useful because Graphite has already worked through
many stacked-PR ergonomics. Gitslice should borrow the workflow lessons without
copying Git branches as the internal model.

Useful lessons:

- Treat each stack entry as an atomic review unit. Graphite describes each branch
  as an atomic changeset and generally treats branches as commit-like units
  ([Create A Stack](https://www.graphite.com/docs/create-stack)). Gitslice should
  keep each stack entry as a normal changeset, not collapse entries into
  patchsets.
- Support trees, not only lines. Graphite navigation docs describe ambiguous
  `up`/`top` movement when a branch has multiple children
  ([Navigate A Stack](https://www.graphite.com/docs/navigate-stack)). Gitslice
  should support multiple children per parent from the start and require a child
  picker when navigation is ambiguous.
- Make restack a first-class operation. Graphite restacks recursive children when
  moving branches or editing mid-stack
  ([Edit The Branch Order In A Stack](https://www.graphite.com/docs/edit-branch-order),
  [Update Mid Stack Branches](https://www.graphite.com/docs/update-mid-stack-branches)).
  Gitslice should model restack as server-visible patchset creation with durable
  conflict metadata rather than hidden Git rebase state.
- Provide explicit move and insert operations. Graphite has `move`, `reorder`,
  and `create --insert` flows for changing dependencies. Gitslice should expose
  `gs stack move` and `gs stack insert`, with subtree restack and conflict
  reporting.
- Keep visualization central. Graphite's `log --stack` focuses on ancestors and
  descendants ([Visualize a stack](https://www.graphite.com/docs/visualize-stack)).
  Gitslice `gs status`, `gs stack show`, and the web stack page should show the
  active entry in its ancestor/descendant context, not as an unstructured list.
- Make partial landing explicit. Graphite's docs describe merging only part of a
  stack and then syncing/restacking remaining branches. Gitslice should allow
  parent or sibling entries to remain accepted/submitted when a later descendant
  fails.
- Make the submit path stack-aware. Graphite's merge queue is stack-aware and can
  validate stacks with optimized batching
  ([Merge Queue](https://www.graphite.com/docs/graphite-merge-queue)). Gitslice
  should use path-head admission and batched publish to keep correctness while
  allowing stack-aware ordering and future optimization.

Important differences:

- Graphite stores stack entries as Git branches and updates them with rebases.
  Gitslice stores entries as native changesets and immutable patchsets, with
  preview trees in object storage.
- Graphite's conflict flow is Git rebase-centered. Gitslice should keep conflict
  state explicit in patchsets and workspace metadata.
- Graphite integrates with GitHub PRs. Gitslice should keep the native API as the
  source of truth and expose Git compatibility later as a projection.

## 8. Git Compatibility

Git compatibility should remain secondary. The first stack implementation can be
CLI and web native only.

Later Git gateway support can map a pushed commit chain into a stack:

```text
git push refs/heads/topic:refs/changes/stack/new
  commit 1 -> changeset @42
  commit 2 -> changeset @43 parent @42
  commit 3 -> changeset @44 parent @43
```

This import path still creates native changesets and patchsets. It must not make
Git commits the internal source of truth.

## 9. Rollout Plan

1. Add storage columns and stack tables behind unused APIs.
2. Teach patchset creation to compute and store `base_tree_id` and
   `result_tree_id` for normal single changesets.
3. Add `ChangesetStackService` and stack-aware validation.
4. Update CLI state handling with backward-compatible single-entry migration.
5. Add `gs stack show`, `gs stack child`, `gs stack switch`, `gs stack move`,
   and `gs stack restack`.
6. Add `gs stack submit` after ordered submit is covered by tests.
7. Add web stack lookup/detail after grpc-gateway exposes the stack service.
8. Add web create/restack/submit actions after the CLI path is stable.

This order keeps existing single-changeset behavior working while the server
learns preview trees and parent links.

## 10. Verification

Server tests:

- create a stack with three entries in one slice
- create a stack tree with one parent and two children
- reject a child from a different slice
- reject a child with a different target ref
- reject a child when `expected_parent_patchset_id` is stale
- update a parent and verify every child subtree needs restack
- move an entry to a new parent and reject cycle-producing moves
- clean restack creates new child patchsets across a subtree
- conflicted restack records `PatchsetConflict` and blocks submit
- submit stack parent-before-child and verify path-head transitions
- partial submit leaves accepted parents and siblings intact when a child fails
- publisher emits a commit chain in admission order

CLI tests:

- migrate old `.gs/state.json` into implicit single-entry state
- create first entry, add child, add sibling, switch entries, update active entry
- show `gs status` stack output
- block switch with unsnapshotted local edits
- prompt for child selection when navigating upward from a multi-child parent
- move an entry and restack its descendants
- restack clean and conflicted descendants across a subtree
- submit stack with `--no-watch` and with polling
- JSON output includes stack fields while preserving existing fields

Web tests:

- stack detail renders entry order, parent links, and active entry detail
- add child, add sibling, move entry, and add patchset are separate actions
- restack result shows clean and conflicted entries
- submit progress shows accepted, pending, submitted, and blocked entries
- changeset detail links back to stack context

## 11. Open Questions

- Should stacks be named objects users can keep after all entries submit, or
  should they auto-close when every entry reaches `submitted` or `abandoned`?
- Should `gs sync` restack automatically when only the target ref moved and no
  parent patchset changed, or should it always require explicit `gs stack
  restack`?
- Should checks run per entry only, or can a future check result cover the whole
  stack when dependency analysis proves the same tested tree?
- Should a parent abandon be allowed with automatic descendant detach, or should
  the user explicitly abandon or restack descendants first?
- Should `gs stack move` default to moving only one subtree, or offer a mode for
  selecting multiple sibling subtrees at once?
