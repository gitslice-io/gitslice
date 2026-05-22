# Gitslice Architecture Design

## 1. Overview

Gitslice, or GS, is a cloud-native, Git-compatible version control system for
large multi-account codebases, repository-like slices, virtual workspaces, and
changeset-based collaboration.

The central architectural idea is:

```text
Native global source graph first.
Git compatibility at the boundary.
```

Gitslice should not be implemented internally as a traditional Git server. Git
clients should see ordinary Git repositories, but the source of truth should be
a scalable native storage and metadata system.

Companion documents:

- [00_product.md](00_product.md): product overview, users, workflows, and scope
- [02_storage.md](02_storage.md): storage, object model, refs, hashing, and replication
- [03_core_api.md](03_core_api.md): gRPC services, proto messages, and gateway behavior
- [04_cli_design.md](04_cli_design.md): native `gs` CLI, workspace behavior, and jj-inspired UX
- [05_git_compatibility.md](05_git_compatibility.md): Git gateway, projected refs, synthetic commits, and push behavior
- [06_indexing.md](06_indexing.md): derived indexes, events, freshness, and rebuilds
- [07_execution_plan.md](07_execution_plan.md): implementation phases and workflow validation

Gitslice is designed to support:

- Account namespaces for users and organizations
- Repository-like slices with their own visibility and access rules
- A single global commit graph across all slices
- Single-slice changesets as the only submission unit
- Sparse, virtualized workspaces
- Changesets as the review and submission unit
- Git clone, fetch, and push compatibility
- Agent-native code workflows
- Large file trees, large histories, and incremental indexing

---

## 2. Design Principles

### 2.1 Accounts Own Top-Level Namespaces

The global namespace is rooted directly under globally unique account slugs:

```text
/{account}/...
```

An account may be a user or an organization, but that type is account metadata,
not part of the path. Account slugs must be globally unique across users and
organizations.

There are no special top-level `/shared`, `/system`, or `/build` namespaces.
Shared libraries, build configuration, generated code, and platform-owned code
should live in a normal account namespace.

### 2.2 Slices Are Repository-Like

A slice is the primary unit of access, visibility, checkout, review, and Git
compatibility.

A slice is similar to a GitHub repository:

- It has an account owner.
- It has a stable slug.
- It has visibility settings.
- It has members and roles.
- It has one or more included absolute paths.
- It can be cloned as a Git repository.

A slice is not an independent storage repository internally. It is a projection
over the global commit graph.

### 2.3 Absolute Paths Everywhere

Every file and directory has one canonical absolute global path.

Example:

```text
/nicholas/services/identity/auth.go
/acme/payment/api/handler.go
```

Slices do not remap paths to custom mount locations. A checkout of a slice
preserves the canonical path layout, minus the leading `/` required by local
filesystems.

Example slice includes:

```text
/nicholas/services/identity
/nicholas/libs/auth
```

Example checkout layout:

```text
identity/
  nicholas/
    services/
      identity/
    libs/
      auth/
```

This removes path aliasing from the core model and keeps Git projection,
authorization, diffs, review, and local workspaces easier to reason about.

### 2.4 Changesets Are The Write Model

Users and agents should not normally write commits directly.

The normal write path is:

```text
workspace diff
  -> patchset
  -> changeset
  -> review and validation
  -> account-defined submit queue or queues
  -> global commit or commits
  -> atomic ref update
```

Storage-level commit creation is an internal implementation detail.

### 2.5 Commits Are Storage Artifacts

Commits are immutable storage-level snapshots of the global tree.

Users interact mostly with:

- slices
- workspaces
- changesets
- patchsets
- reviews
- account-defined submit queues

### 2.6 Git Is A Compatibility Layer

Git should be supported for clone, fetch, push, CI, IDEs, and ecosystem tools.

Git should not define the native data model.

---

## 3. Global Namespace

The repository is one global path namespace under account slugs.

```text
/nicholas/...
/alice/...
/acme/...
/open-source-lab/...
```

Examples:

```text
/nicholas/services/identity
/nicholas/libs/auth
/acme/payment
/acme/proto/payment
/acme/build/bazel
```

The global namespace allows:

- Unified history
- Global indexing
- Global code search
- Cross-slice visibility without cross-slice submission
- Consistent absolute paths for humans, agents, APIs, and Git projections

### 3.1 Account Identity

Account identity includes a stable account id, globally unique slug, and account
kind.

```text
acct_01J...
slug: nicholas
kind: user

acct_01K...
slug: acme
kind: org
```

User and organization slugs may not overlap. Removing typed path prefixes keeps
paths shorter, but it requires a single global slug registry.

Examples:

```text
/acme
```

This path belongs to exactly one account, even if `acme` could otherwise be a
reasonable user or organization name.

### 3.2 Path Ownership

By default:

```text
/{account}/... belongs to account {account}
```

Reserved top-level names such as `.gitslice`, `shared`, `system`, and `build`
cannot be account slugs unless explicitly allowed by a future compatibility
rule.

A slice owned by an account may include paths from that same account.

Cross-account changes are not represented as a single changeset. They require
separate independent changesets, one per affected slice or account workflow. A
slice must not silently mount another account's paths.

Future explicit cross-account collaboration can be added, but it must be modeled
as explicit authorization and import/export behavior, not as a special namespace
or a single cross-account changeset.

---

## 4. Slice Model

A slice is a named, repository-like projection over one or more absolute paths
inside one account namespace.

### 4.1 Slice Identity

Slice identity includes account slug and slice slug.

```text
{account}/{slice}
```

Examples:

```text
nicholas/identity
acme/payment
```

This identity is used in:

- CLI commands
- API requests
- access control
- changesets
- Git URLs
- projection cache keys
- audit logs

### 4.2 Slice Definition

A slice definition is first-class metadata, not an ordinary source file.

Example:

```yaml
id: slc_01J...
account: nicholas
slug: identity
display_name: Identity
default_branch: main
visibility: private

included_paths:
  - /nicholas/services/identity
  - /nicholas/libs/auth
  - /nicholas/proto/identity

roles:
  admins:
    - nicholas
  writers: []
  readers: []
```

The slice definition is versioned and auditable. Each accepted definition change
creates a new slice definition version.

```text
slice_id
slice_definition_version
slice_definition_hash
created_by
created_at
included_paths
visibility
roles
metadata
```

Definition changes are control-plane changes. They should require slice admin
permission and should be recorded with the same review/audit rigor as source
changes. For protected slices, changing `included_paths`, visibility, or roles
should go through a changeset or an equivalent reviewed administrative flow.

### 4.3 Included Paths

`included_paths` are absolute global paths.

They may point to directories or individual files.

```yaml
included_paths:
  - /acme/payment
  - /acme/proto/payment
  - /acme/README.md
```

There are no mount aliases.

The slice checkout and Git projection preserve the absolute path structure
inside the local repository root.

### 4.4 Folder Policy Metadata Files

Write policy is stored at folder scope using reserved metadata files in the
global tree.

Policy file convention:

```text
{folder}/.gitslice/policy.yaml
```

Examples:

```yaml
# /acme/payment/.gitslice/policy.yaml
version: 1
required_approvals:
  - team: payment-owners
required_checks:
  - payment-ci
publicable: false

# /acme/payment/generated/.gitslice/policy.yaml
version: 1
required_checks:
  - generated-code-check
manual_edits: forbidden
```

The policy applies to the folder containing the `.gitslice/policy.yaml` file and
inherits downward. More specific folder policy files may add requirements, but
they should not silently remove requirements from broader folder policy files.

Slice roles remain the coarse membership and visibility model. Folder policies
do not grant read access by themselves; they add submit requirements for matching
paths. If no explicit folder policy file matches a changed path, the default
policy is the slice's normal writer/submit authorization.

Folder policy files are versioned source-tree metadata. Changing one goes
through the normal changeset, review, and submit flow for the folder that owns
the file, but policy changes have stricter authorization than ordinary source
edits. The metadata service indexes these files into policy rules, but the
canonical source of truth remains the file at its global path.

Policy files must not authorize their own weakening. A policy-file change is
validated against:

- the previous accepted policy at the same folder
- every matching ancestor policy file
- account-level policy administration rules

Weakening changes require approval from the parent policy owner or an account
admin, even if the new policy text would remove that requirement. Weakening
includes removing required approvals or checks, broadening write permissions,
making private paths publicable, allowing manual edits where they were
forbidden, or reducing lock requirements.

When a policy file changes, the previous accepted policy remains the validation
authority for that submit. If the same changeset also changes ordinary files
under the affected folder, those file edits must satisfy the stricter union of
the old policy, ancestor policies, and the new policy. The new policy becomes
effective only for later changesets after the policy change lands and is indexed.
Follow-up code changes must not use a just-landed weakening to bypass review of
already-prepared code. When a policy weakening and dependent code rollout are
coordinated outside Gitslice, the code changes should still require explicit
policy-owner approval under the previous policy.

### 4.5 Overlapping Slices

Overlapping slices are supported.

A global path may be included by multiple slices. Each slice gets its own
repository-like projection over the same underlying global objects.

Example:

```yaml
# acme/backend
included_paths:
  - /acme/services
  - /acme/libs

# acme/payment
included_paths:
  - /acme/services/payment
  - /acme/proto/payment
```

In this example, `/acme/services/payment` is covered by both `acme/backend` and
`acme/payment`.

### 4.6 Covering Slices

A covering slice is any slice whose latest accepted `included_paths` contain a
path.

```text
covering_slices(path, definition_epoch)
  -> []slice_id
```

For existing files, coverage is resolved against the file path.

For new files, coverage is resolved against the path that would exist after the
change. A new file is valid only if at least one writable slice covers the new
path.

For renames and moves, coverage is resolved for both the old path and the new
path.

There is no single authoritative governing slice for an overlapping path. The
write policy for a path is the combined policy of all matching ancestor folder
policy files.

The slice through which a user starts work is the authoring slice. It is useful
for UI, defaults, and Git URL resolution, but it does not weaken the policy of
other covering slices.

### 4.7 Overlap Policy Rule

When a single-slice changeset touches paths that are also covered by overlapping
slices, the server computes the union of matching folder policy metadata files.

```text
changed paths
  -> authoring slice containment
  -> covering slices
  -> matching .gitslice/policy.yaml files for each path
  -> union of required roles, approvals, checks, locks, and path rules
```

The safe default is:

```text
Any matching folder policy file may add requirements.
No matching folder policy file may remove another matching policy's requirements.
```

If two matching folder policy files define incompatible requirements, the
changeset is blocked until an authorized policy owner or account admin resolves
the policy conflict.

Examples of compatible policy union:

```text
/acme/services/.gitslice/policy.yaml requires backend-ci
/acme/services/payment/.gitslice/policy.yaml requires payment-owner approval

effective requirement:
  payment-owner approval
  backend-ci
```

Examples of policy conflict:

```text
/acme/services/.gitslice/policy.yaml requires generated file X to be regenerated by tool A
/acme/services/payment/.gitslice/policy.yaml forbids generated file X from being changed manually

resolution:
  blocked until a shared policy or admin override is recorded
```

### 4.8 Slice Definition Overlap Changes

Adding, removing, or moving an included path can change the covering slice set
for many files.

Definition changes that affect overlap must:

- require slice admin permission
- be audited as a new slice definition version
- recompute coverage for affected paths
- recompute matching folder policy files for affected paths
- revalidate open changesets touching affected paths
- invalidate affected projection caches

If a slice definition change adds a new covering slice to an open changeset,
the changeset must recompute matching folder policy files. Any newly required
approvals and checks must be collected before submission.

If a slice definition change removes a covering slice, that slice is removed
from future affected-slice notifications, but the historical review log is
preserved. Folder policy requirements change only when the matching policy files
change.

### 4.9 Slice History Projection

Slice history is a projection of the global commit graph using the latest
accepted slice definition by default.

That means:

```text
slice history = global commits that touched the current included_paths
```

If the slice definition changes, the default projected history can change.

Example:

```text
definition v1 includes:
  /acme/payment

definition v2 includes:
  /acme/payment
  /acme/proto/payment
```

After v2 is accepted, the default slice history includes past global commits
that touched either path.

This is intentional. The slice answers the question:

```text
What is the history of the paths this slice currently includes?
```

For audit and debugging, the system may also support pinned historical
projection:

```text
slice_id + slice_definition_version + global_commit
```

But the normal user-facing history should use the latest definition.

This has an important consequence: slice definition changes can reshape projected
history. The global commit graph remains immutable and linear, but the projected
history for a slice can gain or lose historical commits when `included_paths`
changes.

Git clients must treat a slice definition change as a projection epoch change.
The system should expose the current projection epoch in clone/fetch metadata and
surface a clear sync/reset flow if the projected Git branch is no longer a
fast-forward update for an existing checkout.

### 4.10 Projection Cache Identity

Because slice projection depends on the slice definition, projection caches must
include the definition hash.

```text
(slice_id, slice_definition_hash, global_commit_id) -> projected_tree_id
(slice_id, slice_definition_hash, global_commit_id) -> synthetic_git_commit_id
synthetic_git_commit_id -> global_commit_id
```

When a slice definition changes, the system can invalidate or lazily rebuild
projection cache entries for that slice.

---

## 5. Visibility And Access Control

Visibility and access control are slice-level.

A slice is the unit users reason about, similar to a repository on GitHub.

### 5.1 Visibility

Recommended visibility states:

```text
private
account
public
```

Meaning:

```text
private: visible only to explicitly authorized users and groups
account: visible to members of the owning account
public: readable without authentication
```

### 5.2 Roles

Recommended slice roles:

```text
owner
admin
writer
reader
```

Capabilities:

```text
owner:
  transfer/delete slice
  manage admins
  manage all settings

admin:
  manage visibility
  manage readers/writers
  change included paths
  approve protected changes

writer:
  create changesets
  push to changeset refs
  submit when policy allows

reader:
  clone/fetch/read slice contents
  view changesets
```

### 5.3 Included Path Authorization

Changing `included_paths` is a privileged slice administration action.

Validation rules:

- A slice may include only paths under its owning account root: `/{account}/...`.
- Included paths may overlap other slices in the same account.
- Cross-account included paths are not allowed in the initial design.
- Folder policy metadata files must live under the account namespace they govern.
- Public visibility cannot expose folders that account policy or matching folder
  policy file marks as non-publicable.

### 5.4 Changeset Authorization

A changeset is scoped to exactly one authoring slice. Every changed path must be
included in that authoring slice. A changeset cannot directly span multiple
slices or accounts.

For each changed global path, the server resolves all covering slices. This is
still necessary because slices can overlap. Overlap can add policy requirements,
but it does not make the changeset a cross-slice changeset.

```text
changed paths
  -> covering slices
  -> matching folder policy files
  -> required slice roles
  -> required approvals
  -> required checks
```

Cross-slice changesets are not allowed. Work that logically spans slices must be
split into separate independent changesets, each scoped to its own authoring
slice. Gitslice does not provide a linked-changeset object or atomic
coordination layer in the initial design.

Cross-account changesets are not allowed in the initial design.

Default write authorization:

- A user may create a changeset from a slice where they have writer access.
- The user must have read access to every path they modify.
- Other covering slices do not add writer-role requirements by default.
- Submission requires every matching folder policy file to be satisfied.

### 5.5 Overlap Read Visibility

Read access is evaluated through the slice being read.

If a public slice includes a path, that path is publicly readable through that
slice. A private overlapping slice cannot make the same underlying bytes private
again.

Effective exposure for a global path is therefore the broadest visibility of any
covering slice.

```text
private + public overlap -> path is public through the public slice
```

For that reason, changing a slice to `public` must analyze every included path
and surface any overlapping slices before the visibility change is accepted.

Changeset and review UIs should filter or redact file content per reader. A user
who can read only one affected slice may see the paths and diffs they are
authorized for, while hidden paths remain redacted unless the user can read the
other affected slices.

---

## 6. Workspace Model

A workspace is a local hydrated development environment over one or more slices.

Workspaces are sparse and virtualized. Users should not need to clone the entire
global namespace.

Example:

```bash
gs workspace init
gs slice add nicholas/identity
gs slice add acme/payment
```

Example workspace layout:

```text
workspace/
  nicholas/
    services/
      identity/
    libs/
      auth/
  acme/
    payment/
  .gs/
```

The client maintains:

```text
workspace config
slice bindings
metadata cache
hydrated file cache
overlay changes
changeset state
local operation log
draft patchset snapshots
```

Files are hydrated on demand.

The workspace can contain multiple slices, but each file path still has one
canonical absolute global path.

The detailed native CLI and local workspace behavior is defined in
[04_cli_design.md](04_cli_design.md).

---

## 7. Changeset Model

A changeset is the collaboration and submission object.

A changeset represents a proposed change to the global source graph through one
authoring slice. It cannot directly span multiple slices or accounts. Work that
must move together across slices is split into independent changesets and
coordinated outside the changeset model.

### 7.1 Changeset Structure

```text
Changeset:
  id
  author
  authoring_slice
  created_at
  updated_at
  target_ref
  base_commit
  patchsets[]
  current_patchset
  affected_paths[]
  covering_slices_by_path[]
  folder_policy_files_by_path[]
  expected_slice_definition_hashes[]
  required_queues[]
  expected_queue_definition_hashes[]
  required_policy_files[]
  status
  review_state
  test_state
  metadata
```

### 7.2 Patchsets

A patchset is one immutable revision of the file changes inside a changeset.
The changeset is the long-lived review and workflow object; patchsets are the
successive versions of the proposed diff.

```text
CS123
  patchset 1: initial diff
  patchset 2: updated after review feedback
  patchset 3: rebased onto a newer target ref
```

Each user or agent update to a changeset creates a new patchset instead of
mutating the previous one. This gives reviews, approvals, checks, conflict
analysis, and audit logs a stable object to refer to.

A patchset is not a Git commit and is not only a textual `.patch` file. It is a
native representation of a proposed tree change:

```text
Patchset:
  id
  changeset_id
  number
  base_commit
  created_at
  author
  changed_paths[]
  file_edits[]
  resulting_tree_preview
  covering_slices_by_path[]
  folder_policy_files_by_path[]
  expected_slice_definition_hashes[]
  required_policy_files_snapshot
```

Patchsets store changes using canonical global paths. They do not depend on a
mount alias or local checkout layout.

```text
/acme/payment/api/handler.go
/acme/proto/payment/payment.proto
```

The current patchset is the version that would be submitted if the changeset
lands. Older patchsets remain available for review history, auditability, and
comparison.

Approvals and checks should record which patchset they evaluated. When a new
patchset changes affected paths, covering slices, matching folder policy files,
or file content, the server may invalidate or refresh approvals and checks
according to folder policy files and queue policy.

### 7.3 Changeset Lifecycle

```text
Draft
  -> Review
  -> Queued
  -> Submitting
  -> Submitted
```

Other states:

```text
Abandoned
Failed
MergeConflict
NeedsRebase
```

### 7.4 Changeset To Commit Mapping

A changeset is not necessarily equal to a single commit.

Possible mappings:

```text
1 changeset -> 1 global commit
1 changeset -> N global commits
N changesets -> 1 squashed global commit
```

The user-facing object remains the changeset.

A changeset does not coordinate file edits across independent slices. The
atomicity boundary is the metadata transaction for a single target ref update.
Work that spans slices or accounts should be split into independent changesets
that submit under each slice's policy.

### 7.5 No Direct User Commits

The public API should not expose a generic "create commit" operation as the
normal write path.

Allowed user write paths:

```text
create changeset
update changeset
submit changeset
abandon changeset
```

Internal services may create commits only as part of submit, import, migration,
or trusted administrative workflows.

---

## 8. Storage, Commits, And Refs

Commits are immutable storage-level snapshots of the global tree. Refs are
mutable named pointers to commits and move only through conditional atomic
updates.

Detailed storage, object, path hashing, ref, and replication design lives in
[02_storage.md](02_storage.md).

The architectural summary is:

```text
Ref -> Commit -> RootTree -> TreeEntries -> Blobs
```

Everything except refs is immutable. Submit workers publish accepted changes by
creating commits and moving a target ref with CAS.

---

## 9. Git Compatibility

Git compatibility is implemented as a projection layer. Each slice can be exposed
as a Git repository, but Git is not the native storage model.

The detailed Git gateway design lives in
[05_git_compatibility.md](05_git_compatibility.md).

The architectural summary is:

- clone and fetch project native commits and trees into Git objects
- Git refs are compatibility views over native refs
- Git commits are synthetic and stable for the same projection inputs
- protected pushes create or update changesets instead of directly moving
  accepted refs
- Git-originated writes must satisfy the same slice, folder policy, queue, and
  validation rules as native writes

---

## 10. Core API

Native APIs are gRPC-first. HTTP endpoints should be exposed through
grpc-gateway bindings where needed.

The detailed gRPC service and message definitions live in [03_core_api.md](03_core_api.md).

The architectural summary is:

- reads resolve commits, paths, directory entries, and file streams
- writes are changeset-oriented for normal users and agents
- blob upload is staged before submit
- direct commit creation is internal and must not bypass validation
- Git compatibility is implemented by a gateway that translates Git operations
  into core API calls

---

## 11. Conflict Prevention

Gitslice should use optimistic concurrency control by default.

Every changeset is based on a specific global commit.

```text
Changeset:
  base_commit = G100
```

Before submission, the server validates:

```text
Can the patch apply cleanly to current head?
Do affected paths still have the expected covering slices?
Does the author still have the required slice roles?
Do all matching folder policy files pass?
Do required checks pass on the latest head?
```

### 11.1 Conflict Types

File content conflict:

```text
Two changes edit the same lines.
```

Path conflict:

```text
One changeset deletes or renames a file while another edits it.
```

Slice coverage conflict:

```text
The covering slice set or included path set changed while the changeset was open.
```

Overlap policy conflict:

```text
Two matching folder policy files impose incompatible requirements on the same path.
```

Semantic conflict:

```text
Two changes touch different files but break behavior together.
```

Semantic conflicts are handled by tests and the required queue or queues.

### 11.2 Overlap Conflict Resolution Process

Overlapping slices are resolved by recomputing coverage and applying the union of
all matching folder policy files at every important transition.

Process:

```text
1. Create or update patchset.
2. Normalize changed paths to canonical absolute paths.
3. Verify every changed path is included in the authoring slice.
4. Resolve covering slices for each changed path using latest slice definitions.
5. Resolve matching folder policy files for each changed path.
6. Store covering_slices_by_path, matching policy file paths, policy file hashes,
   and slice definition hashes on the patchset.
7. Compute required approvals, roles, locks, checks, and path rules from all
   matching folder policy files.
8. Notify reviewers for every affected covering slice or policy owner.
9. Collect approvals per matching folder policy file.
10. Before submit, recompute coverage and policy files against latest definitions.
11. If coverage or policies changed, refresh requirements before continuing.
12. Reapply patch to latest target ref.
13. Run required checks.
14. Publish commit and update target ref with CAS.
```

Coverage refresh outcomes:

```text
unchanged:
  keep current requirements and continue

covering slice added:
  update affected-slice notifications and recompute matching policy files

covering slice removed:
  remove future affected-slice notifications but preserve historical review log

folder policy file changed:
  recompute requirements; stale approvals may need renewal if policy file requires it

included path moved:
  mark NeedsRebase or NeedsPolicyRefresh depending on whether the patch still applies
```

The changeset should show coverage explicitly.

Example:

```text
/acme/services/payment/handler.go
  covering slices:
    acme/backend
    acme/payment
  matching policy files:
    /acme/services/.gitslice/policy.yaml
    /acme/services/payment/.gitslice/policy.yaml
  required:
    backend-ci
    payment-owner approval
```

### 11.3 Concurrent Overlap Changes

Two changesets from different authoring slices can edit the same overlapping
path.

They do not merge independently per slice. Queue selection resolves every
covering slice, then places both changesets into the required account queues. If
they share any required queue, that queue serializes them.

If the first changeset lands, the second changeset must reapply to the new head.
If the patch no longer applies cleanly, it becomes `NeedsRebase` or
`MergeConflict`.

### 11.4 Approval Semantics

Approvals are recorded against both:

```text
slice_id
slice_definition_hash
policy_file_path
policy_file_hash
```

An approval remains valid only while the relevant slice definition and matching
folder policy file remain valid for the affected paths, unless the policy file
explicitly allows stale approvals.

If a new covering slice appears, that slice has not approved the change yet.

If a covering slice disappears, its approval is retained in the audit log but is
not required for the next submit attempt.

### 11.5 Incompatible Policy Resolution

Most policies compose by union. Some policies can conflict.

When policies conflict, the changeset cannot submit automatically.

Resolution options:

```text
1. Update one or more folder policy files.
2. Split the changeset so conflicting paths are reviewed separately.
3. Apply an explicit admin override if the account allows overrides.
4. Abandon the changeset.
```

Admin overrides must be audited and should name the conflicting policies they
override.

---

## 12. Versioned Submit Queues

Gitslice does not have one global submit queue.

Each account owns versioned queue definitions under its namespace. Queue
definitions decide how changes touching that account's slices are ordered,
validated, and submitted.

Queue file convention:

```text
/{account}/.gitslice/queues/{queue}.yaml
```

Example:

```yaml
version: 1
name: main
target_ref: refs/global/main

scope:
  paths:
    - /acme/**
  slices:
    - acme/*

ordering: fifo

submit:
  required_roles:
    - writer
  required_approvals:
    - team: acme-maintainers
  required_checks:
    - acme-ci

concurrency:
  max_active: 1
  allow_disjoint_paths: false

overrides:
  admin_override: true
```

Queue files are ordinary versioned source graph files. Updating a queue file is a
control-plane change and should itself go through a changeset. The effective
queue configuration for submission is resolved from the latest accepted target
ref at the time the changeset is validated.

If an account has no queue file yet, the system provides a bootstrap default
queue:

```text
/{account}/.gitslice/queues/default.yaml
```

The bootstrap queue exists as system behavior until the account commits an
explicit queue file.

### 12.1 Queue Selection

Queue selection happens after changed paths and covering slices are resolved.

```text
changed paths
  -> authoring slice containment
  -> covering slices
  -> authoring account
  -> queue rules from that account's .gitslice/queues/*.yaml
  -> required queues
```

If more than one queue matches, the changeset must satisfy all of them.

For the initial design, all required queues for one changeset must agree on the
same `target_ref`. If they do not, the changeset is a queue conflict and cannot
submit until it is split, retargeted, or the queue files are changed.

Examples:

```text
/acme/services/payment/handler.go
  covering slices:
    acme/backend
    acme/payment
  required queues:
    /acme/.gitslice/queues/backend.yaml
    /acme/.gitslice/queues/payments.yaml
```

Queue selection records:

```text
queue_id
queue_definition_hash
target_ref
matched_paths
matched_slices
required_checks
required_approvals
```

### 12.2 Single-Queue Submit

For a changeset assigned to one queue:

```text
1. Wait until the changeset is runnable in that queue.
2. Lease the queue item.
3. Load latest queue definition from the target ref.
4. Recompute changed paths, covering slices, and queue selection.
5. Refresh approvals, roles, locks, and checks.
6. Run required checks.
7. Hand off to the target-ref landing sequencer.
8. Rebase or reapply onto the latest target ref inside the sequencer lease.
9. Revalidate freshness, policy, queues, checks, and conflicts.
10. Create final commit or commits.
11. Atomically update target ref with CAS.
12. Emit indexing events for every affected covering slice.
```

If CAS fails despite the sequencer lease, the worker treats it as a stale
sequencer/admin-intervention conflict, reloads the new head, and returns the
changeset to a retryable queue state. It should not spin in an unbounded CAS
retry loop.

### 12.3 Multi-Queue Submit

A changeset can require multiple queues when it touches overlapping slices or
when multiple queue rules match within the authoring account.

Multi-queue submit coordinates policy and ordering across the affected queues.
It is not a cross-slice or cross-account submit protocol. For the initial design,
a single submit still updates only one `target_ref`; work that needs independent
slice or account workflows should be split into independent changesets.

Multi-queue submit uses deterministic queue leases.

```text
1. Compute required queue set.
2. Sort queue ids lexicographically.
3. Wait until the changeset is runnable in every required queue.
4. Acquire leases in sorted order.
5. Revalidate queue definitions and covering slices.
6. Run union of required checks.
7. Hand off to the target-ref landing sequencer.
8. Reapply patch to latest target ref inside the sequencer lease.
9. Revalidate freshness, policy, queues, checks, and conflicts.
10. Commit and CAS-update target ref.
11. Release all leases.
```

Sorted lease acquisition prevents deadlocks.

For the MVP, a changeset assigned to multiple queues should be runnable only
when it is at the head of every required queue. This is conservative but easy to
reason about. Later, queues can allow disjoint-path concurrency when their queue
files opt in.

### 12.4 Queue Definition Changes

Queue files are versioned. When a queue file changes:

- new changesets use the new queue definition
- open changesets recompute queue selection before submit
- approvals tied to the old queue definition may need renewal
- queued items whose required queue set changed move to `NeedsQueueRefresh`

Queue config changes should not mutate already-submitted history. They affect
future validation and future submit attempts.

Queue files are policy-bearing metadata. Changes that remove required checks,
remove approvals, broaden matching rules, or retarget a queue must be validated
against the previous accepted queue definition and account-level queue
administration rules. A queue file must not authorize its own weakening.

### 12.5 Queue Conflicts

Queue conflicts happen when queue definitions disagree.

Examples:

```text
queue A requires check acme-ci
queue B forbids external CI for the same path

queue A targets refs/global/main
queue B targets refs/accounts/acme/release
```

Resolution options:

```text
1. Update one or more queue files.
2. Split the changeset.
3. Retarget the changeset if policy allows.
4. Apply an audited admin override.
5. Abandon the changeset.
```

### 12.6 Target-Ref Landing Sequencer

Account queues remove the global queue bottleneck, but independent queues can
still target the same accepted ref. Correctness requires one final
linearization point per `target_ref`.

The Submit Queue Service owns a fair target-ref landing sequencer for each
target ref. Queue workers may evaluate eligibility, approvals, and checks in
parallel, but final landing for a target ref happens through the sequencer.

Sequencer responsibilities:

- choose among ready queue items fairly across required queues
- acquire a short lease for the target ref
- reload the latest target-ref head
- rebase or reapply the patchset
- recompute changed paths, covering slices, policy files, and queue selection
- verify that approvals, locks, checks, and queue eligibility are still fresh
- publish the commit and move the ref with CAS

The sequencer is not a global submit queue. It only serializes the final commit
publication step for one target ref. Queues still define policy, ordering, and
eligibility before handoff.

### 12.7 Why Queues Still Need CAS

Account queues remove the global queue bottleneck, but they do not remove the
need for atomic ref updates.

Two independent queues can land disjoint changes against the same target ref at
roughly the same time if a sequencer lease expires, an admin operation moves the
ref, or a worker observes stale state. CAS ensures only one writer wins the exact
head it validated against. The losing submitter returns to a retryable queue
state and must rerun freshness validation before trying again.

This gives the system both:

- account-defined queue policy
- global commit/ref correctness

---

## 13. Optional Path Locks

Gitslice should avoid locks for normal source development.

Explicit locks may still be useful for rare high-risk paths.

Examples:

```bash
gs lock /acme/infra/prod
gs lock /acme/releases/2026-Q2.yaml
```

Use locks for:

- large binary files
- critical infrastructure config
- generated snapshots
- schema migrations
- release manifests

Path locks do not replace changesets, review, or submit validation.

---

## 14. Indexing System

Indexes are derived data and should be incremental, event-driven, and
rebuildable from source-of-truth objects.

The detailed index catalog, event pipeline, freshness model, and rebuild rules
live in [06_indexing.md](06_indexing.md).

---

## 15. Build And CI Integration

Gitslice should integrate with scalable build and CI systems.

Recommended systems:

- Bazel
- Buck2
- Pants
- ordinary CI runners for smaller slices

Required capabilities:

- affected target calculation
- remote execution where available
- remote caching where available
- test impact analysis
- hermetic builds where practical
- build graph indexing

Submission policies should be able to reference required checks.

Example:

```yaml
submit:
  required_owners:
    - identity-team

  checks:
    - //nicholas/services/identity/...
    - //nicholas/proto/identity/...
```

---

## 16. Service Architecture

Core services:

```text
Object Store
Metadata Service
Slice Service
Workspace Service
Git Gateway
GS API Gateway
Changeset Service
Submit Queue Service
Index Service
Build/CI Service
Auth Service
Replication Service
```

### 16.1 Object Store

Stores file contents and large binary objects.

### 16.2 Metadata Service

Stores trees, commits, refs, slice definitions, changesets, and object metadata.

### 16.3 Slice Service

Manages slice definitions, slice resolution, visibility, roles, included paths,
folder policy metadata indexes, and projections.

### 16.4 Workspace Service

Provides backend helpers for workspace metadata, sparse hydration, diff
validation, and optional workspace operation records. The CLI remains
responsible for local workspace files, local cache, and local undo behavior. See
[04_cli_design.md](04_cli_design.md).

### 16.5 Git Gateway

Implements Git smart HTTP and translates between Git objects and native objects.

### 16.6 GS API Gateway

Implements the native GS protocol used by the CLI, web app, SDKs, and agents.

### 16.7 Changeset Service

Manages changesets, patchsets, review state, and workflow state.

### 16.8 Submit Queue Service

Evaluates versioned account queue definitions, manages queue membership and
leases, coordinates submissions that require multiple queues, and performs final
validation before CAS ref updates.

### 16.9 Index Service

Maintains search, symbol, path history, slice coverage, build, and projection
indexes. See [06_indexing.md](06_indexing.md).

---

## 17. Replication Architecture

Replication requirements are part of the storage design. See
[02_storage.md](02_storage.md#8-replication-architecture).

---

## 18. System Invariants

These invariants must not be violated.

```text
1. A committed tree is immutable.
2. A committed blob is immutable and content-addressed.
3. A commit points to exactly one root tree.
4. A ref update is atomic and conditional.
5. A single target-ref submit either publishes all final commits and moves that target ref, or publishes none.
6. Queue definitions are versioned files under account namespaces.
7. A changeset must submit through every queue selected by its affected paths and covering slices.
8. Multi-queue submit must acquire queue leases in deterministic order.
9. A slice projection is deterministic for a given slice id, slice definition hash, and global commit.
10. Default slice history uses the latest accepted slice definition.
11. Slice visibility and roles govern access to all paths included by the slice.
12. A global path may be covered by multiple slices.
13. Writes to overlapping paths must satisfy every matching folder policy file at submit time.
14. Effective read exposure for a path is the broadest visibility of any covering slice.
15. Git synthetic commit IDs are stable for the same projection inputs.
16. Metadata must never reference an unverified blob.
17. Derived indexes can be rebuilt from commits, trees, blobs, slice definitions, folder policy files, and queue definitions.
```

---

## 19. Execution Plan

Implementation phases and workflow validation have moved to
[07_execution_plan.md](07_execution_plan.md).

---

## 20. Non-Goals For The Initial Design

The initial design should not include:

- special `/shared` or `/system` namespaces
- custom mount aliases inside slices
- direct user-facing commit creation
- single-owner path model
- object-store participation in metadata transactions
- path-level ACLs as the primary access model
- Git-native storage internals
- distributed atomic commits across slices or target refs

These can be revisited only if a concrete product requirement justifies the
additional complexity.

---

## 21. Long-Term Direction

Gitslice should become a source graph platform with:

- Git-compatible slice repositories
- repository-like access control
- global-scale history and indexing
- sparse workspaces for humans and agents
- changeset-centered collaboration
- single-slice changeset submission
- native cloud storage and metadata architecture

The architecture should stay simple at the conceptual boundary:

```text
global paths
slice coverage
changesets
immutable commits
atomic refs
Git projection
```
