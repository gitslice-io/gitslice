# Dependent Changesets Design

This document replaces the earlier public "stacked changesets" design. Gitslice
still needs the workflow that stacked-review tools provide: a user can split a
larger change into multiple reviewable changesets, make one changeset depend on
another, update descendants when a base changeset changes, and submit in
base-before-dependent order.

The product model should not expose "stack" as a first-class concept. Users work
with changesets, base changesets, and dependent changesets.

## 1. Goals

- Allow a changeset to be based on either a commit or another changeset's
  current patchset.
- Let users create, inspect, move, update, and submit dependent changesets
  without managing a named stack object.
- Keep each changeset as the review unit with its own patchsets, approvals,
  checks, status, and submit result.
- Preserve single-slice behavior. A dependent changeset must stay in the same
  authoring slice and target ref as its base changeset.
- Make dependency state durable enough for server-side validation, preview
  trees, conflict detection, and ordered submit.

## 2. Non-Goals

- Cross-slice dependent changesets.
- Treating patchsets as review objects. A patchset remains a revision of one
  changeset.
- Exposing a separate "stack" tab, route, command namespace, or named object.
- Git branch compatibility for dependent changesets in the first pass.

## 3. Product Vocabulary

Use these terms in UI, CLI, docs, and API comments:

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

Avoid public labels such as `stack`, `stack entry`, `stack id`, `Stack submit`,
or `Restack`. Internal code may continue using existing names until the storage
and generated API surface are renamed.

## 4. Core Model

The durable user-visible model is a changeset graph constrained to a same-slice
tree:

```text
Changeset:
  id
  authoring_slice
  target_ref
  base_commit_id
  base_kind                    # commit or patchset
  base_changeset_id            # set for dependent changesets
  base_patchset_id             # base patchset recorded at creation/update
  dependency_order
  dependency_depth
  sibling_order
```

Patchsets record the actual materialization base:

```text
Patchset:
  changeset_id
  base_commit_id
  base_kind                    # commit or patchset
  base_patchset_id             # set when base_kind is patchset
  base_tree_id                 # tree read by validation and diff preview
  result_tree_id               # materialized result tree
```

For a root changeset, `base_kind = commit` and `base_tree_id` is the root tree
for `base_commit_id`. For a dependent changeset, `base_kind = patchset`,
`base_patchset_id` points at the base changeset's current patchset at the time
the dependent patchset was created, and `base_tree_id` is that base patchset's
`result_tree_id`.

The dependency tree is derived from `base_changeset_id`. A separate named
review object is not part of the product model.

## 5. Validation Rules

Creation and mutation must enforce:

1. Base and dependent changesets are in the same authoring slice.
2. Base and dependent changesets use the same target ref.
3. A dependent changeset's base patchset must be the base changeset's current
   patchset unless the request is explicitly updating stale descendants.
4. No cycles.
5. Only one root is needed for a dependency tree, but users should not need to
   name or manage that tree.
6. Submitted changesets are immutable anchors. Dependents may update their base
   to a submitted base changeset's result, but a submitted changeset cannot be
   moved.
7. A changeset with non-terminal dependents cannot be abandoned unless the
   request explicitly moves, updates, or abandons the descendants.
8. Submit must reject a dependent changeset when its required unsubmitted
   ancestors are not submitted in the same operation.
9. Submit must reject dependents whose `base_patchset_id` no longer matches the
   base changeset's current patchset.

## 6. API Shape

The primary API should hang off `ChangesetService`, not a public stack service.
The preferred surface is:

```protobuf
message CreateChangesetRequest {
  SliceRef authoring_slice = 1;
  string target_ref = 2;
  string base_commit_id = 3;
  string title = 4;
  string description = 5;
  string base_changeset_id = 6;
  string base_patchset_id = 7;
}

message ListChangesetsRequest {
  SliceRef authoring_slice = 1;
  string status = 2;
  int32 limit = 3;
  bool include_dependency_metadata = 4;
}

message GetDependencyTreeRequest {
  string changeset_id = 1;
}

message DependencyTree {
  string root_changeset_id = 1;
  repeated DependencyTreeEntry entries = 2;
}

message DependencyTreeEntry {
  string changeset_id = 1;
  string base_changeset_id = 2;
  string base_patchset_id = 3;
  int64 sibling_order = 4;
  int64 display_order = 5;
  int64 depth = 6;
  string state = 7;
  Changeset changeset = 8;
}

message UpdateDependentsRequest {
  string changeset_id = 1;
  bool include_siblings = 2;
  string target_base_commit_id = 3;
}

message SubmitWithDependenciesRequest {
  string changeset_id = 1;
  bool include_descendants = 2;
}
```

Implementation may initially delegate to existing storage and service helpers,
but public names should be dependency-oriented.

## 7. Diff And Preview Semantics

Diff base is a view over the stored patchset base. Users may compare arbitrary
patchsets in the diff viewer, but changing a changeset's base is a data mutation
that creates a new patchset or updates dependency metadata.

Rules:

- `DiffChangeset` defaults to the selected patchset against its recorded base.
- Comparing two patchsets within one changeset remains supported.
- Updating the base changeset creates a new dependent patchset derived from the
  new base patchset's result tree.
- Preview trees and submit validation always use the stored patchset base, not a
  transient UI diff base.

## 8. CLI Design

The top-level workflow should be changeset-first:

```text
gs create --message <title> [--base <changeset>] [--root] [--sibling]
gs modify [--all] [--message <title>] [--no-update-dependents]
gs submit [changeset] [--with-dependencies] [--subtree <changeset>] [--no-watch]
gs deps [changeset]
gs update-dependents [changeset] [--children|--all]
gs switch <changeset>
gs up [changeset]
gs down [steps]
gs top
gs bottom
gs move <changeset> --onto <base|root>
gs insert --base <changeset> --message <title>
gs detach <changeset>
```

Text output should say `dependency tree`, `base changeset`, `dependent
changeset`, and `needs update`. Machine output can keep existing internal fields
until the schema version is bumped, but new schema docs should prefer
`dependency_tree_id`, `dependency_state`, and `dependent_changesets`.

## 9. Web Design

The web UI should not have a separate Stacks tab. The Changesets area owns
dependent review:

- `/changesets` lists changesets for a slice.
- A dependency view can be reached from a changeset row or detail page.
- The detail page shows `Based on` and `Dependents`.
- A dependency tree page is acceptable, but it should be labeled as dependency
  review and reached as part of changeset workflow.
- Actions should be named `Create dependent changeset`, `Update dependents`,
  `Move to new base`, and `Submit with dependencies`.

Routes should use dependency terminology:

```text
/dependencies
/dependencies/new
/dependencies/{id}
/dependencies/{id}/update
/dependencies/{id}/submit
/cs/{id}?dependency={dependency_tree_id}
```

The route id may still reference the current backing record during migration,
but the UI must not ask the user to understand or manage a stack object.

## 10. Storage Plan

The first implementation can keep the existing physical tables if that keeps the
change low-risk:

```text
changeset_stacks
changeset_stack_entries
changesets.stack_id
changesets.parent_changeset_id
changesets.parent_patchset_id
patchsets.base_patchset_id
```

These names should be treated as internal implementation details. A later
storage migration can rename them to dependency-oriented names once the API and
client terminology has settled.

If storage is renamed later, migrate as follows:

1. Create dependency-oriented tables/columns.
2. Backfill from existing stack rows.
3. Update services to write both old and new fields for one deploy.
4. Read from dependency fields.
5. Drop old fields after verification.

Because backward compatibility is not a product requirement right now, API and
UI can move directly to dependency terminology before the physical rename.

## 11. Verification

Local verification for product-surface changes:

```bash
npm --prefix web test -- --run
npm --prefix web run build
go test ./internal/cli ./service ./tests/rpc
go build ./cmd/...
```

Full server verification when submit, update-dependent, or storage behavior
changes:

```bash
set -a
. ./env.local
set +a
go test -count=1 ./tests/cli ./tests/rpc -v
```

Add focused coverage for:

- creating a dependent changeset from a base changeset
- rendering dependency trees without public stack terminology
- updating stale dependents after a base patchset changes
- submitting ancestors before descendants
- CLI help and schema output using dependency terminology

## 12. Resolved Decisions

- Do not expose a Stack product concept.
- Use base/dependent changeset terminology across docs, CLI, and web UI.
- Treat diff base as display/compare state; stored base patchsets remain the
  validation source of truth.
- Keep same-slice dependency constraints.
- Keep physical stack storage as an internal implementation detail for the
  immediate change.
