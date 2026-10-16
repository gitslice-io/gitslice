# Slice Lifecycle: Rename, Archive, Retire

A slice used to have two states: it existed, or it had been deleted, and
deleting was refused once it had a changeset. Someone who named a slice badly
could not rename it, and the old one lingered in their listing for good (agent
feedback, 2026-10-15). This adds a rename, an archive, and a way to delete a
slice that has history.

## Rename

`gs slice rename <slice> <new-name>` / `SliceService.RenameSlice`. Requires
admin on the slice's account. The home slice cannot be renamed.

- The slice id, definition, members, secrets, tags and changesets are
  untouched. The two tables that repeat the slice's name next to its id
  (`changesets`, `changeset_stacks`, `git_imports`) are updated in the same
  transaction.
- The old name becomes a row in `slice_aliases(account_id, slug, slice_id)`.
  `Resolve` falls back to it, so anything that names a slice (the CLI, workspaces
  made before the rename, `git clone .../git/<account>/<old>.git`, `go get`,
  web URLs) still finds it. The response carries the canonical ref, and the web
  page moves to the current address.
- An alias stays reserved forever: no new slice and no rename can take the
  name, except the same slice taking it back. Slice ids are derived from the
  name when a slice is created (`slice_<account>_<slug>`), so a freed name would
  collide with the renamed slice's id.
- `Slice.former_names` lists the aliases.
- The Git projection is cached per requested name, so the old and the new URL
  each build their own cache from the same history and serve the same commits.
  The e2e test checks both clones have the same `HEAD`.

`gs slice rename`, run inside a workspace of the slice, also rewrites the
workspace's `.gs/slice.json`. Other workspaces keep the old name, which works.

## Archive

`gs slice archive` / `unarchive` / `SliceService.SetSliceArchived`
(`slices.archived_at`). Requires admin on the account; the home slice cannot be
archived.

- Hidden from listings (`ListSlices`, public and shared lists) unless
  `include_archived` / `gs slice list --archived`.
- Read-only: `authz.Authorize` refuses `ActionWrite` on an archived slice
  (`FailedPrecondition`: "archived and read-only"). Reads, history and admin
  actions such as unarchive are unaffected.
- The name stays reserved and the slice keeps covering its paths, so its
  required checks still apply to changes to those paths from other slices.

## Delete a slice that has changesets

A slice with no changesets is deleted for good, as before. One with changesets
is now retired (`slices.deleted_at`) when:

1. none of its changesets is open (every one is landed or abandoned), and
2. each path it includes is either covered by another slice that is not
   archived, or holds no files at the head of `refs/global/main`.

Retiring keeps the slice row, so its changesets, its name and its id stay
valid; the slice no longer resolves, lists or covers paths, and its secrets are
deleted. A changeset of a retired slice is still readable (the changeset and
stack read paths look the slice up as it was), so "history stays on the path"
holds. The account's home slice covers every path under the account, so in
practice the second condition holds for any slice inside an account; it guards
the unusual cases.

## Not done

- A retired slice cannot be restored, and its name cannot be reused.
- Webhook payloads and Git tags already sent keep the name they had.
- Renaming an account is a separate problem.
