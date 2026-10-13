# Slice Members

Sharing one slice with another person or agent used to take an organization:
an agent's own account takes no members, and a slice covers only its account's
paths. Slice members let a slice's admins give someone a role on that one
slice, directly, without adding them to the account.

## Model

- A slice member has a role on one slice: `reader`, `writer` or `admin`
  (`slice_members`, migration 0034). It adds to whatever the account gives
  them, and reaches nothing else in the account.
- Access to a slice is the higher of the subject's account role and slice
  role (`authz.Authorize`). Every slice-scoped operation goes through that
  check: reading files, workspaces, Git clone and push, changesets and
  approvals, tags, webhooks.
- What each role allows on the slice:
  - **reader**: read it, check it out (`gs init`), clone it.
  - **writer**: also create, submit and approve changesets, push with Git, and
    tag. Its changesets run the slice's checks, with the slice's secrets, as an
    account writer's do; share writer access with the same trust.
  - **admin**: also manage the slice's members and webhooks.
- What stays with the **account's** owners and admins, whatever the slice
  role (`authorizeSliceOwner`): the slice's definition (included paths,
  visibility, required approvals and checks), its CI daemon, its secrets, and
  deleting it. A slice admin who could widen the paths would reach past the
  slice, and one who could point it at their own CI daemon would receive its
  secrets.
- Adding is direct: the slice's admins add a username and it takes effect at
  once. There is no invitation step; whoever is added can leave.

## Reading files by path

Reads by path rather than through a slice (the CLI's checkout, `gs log`) are
authorized by the account. A signed-in subject outside the account may also
read a path when a slice that includes it is readable to them: shared with
them, or public (`ensurePathRead`, using `CoveringIDsByPath`). Anonymous
readers of a public slice still read through the slice itself. This also
fixed `gs init` of a public slice by someone outside its account, which used
to be refused. A commit read by path (`ensureCommitRead`) is readable when
each path it changes is.

## API and CLI

`SliceService`:

- `ListSliceMembers(slice)`: for anyone with a role on the slice (in the
  account or on the slice). A public slice's other readers cannot list them.
- `SetSliceMember(slice, username, role)`: slice admins. Setting again
  changes the role and keeps who added them.
- `RemoveSliceMember(slice, username)`: slice admins; an empty username is the
  caller leaving.
- `ListSharedSlices()`: the slices shared with the caller, with their role.

`ListSlices(account)` for someone outside the account lists the public slices
and those shared with them.

```bash
gs slice share alice/project bob [--role reader|writer|admin]   # default writer
gs slice members alice/project
gs slice unshare alice/project bob
gs slice leave alice/project                                    # as bob
gs slice list            # your account's slices, then "shared with you"
gs slice list --shared   # only the shared ones
gs auth status           # your accounts with their roles, then "shared with you"
```

Sharing a `home` slice shares everything in the account; the CLI says so.

`gs account members <account>` for an account the caller is not in still
answers not found (the server does not say whether the account exists), but
when slices of that account are shared with the caller the CLI names them and
their roles instead, so an agent sees why it can work in `alice:project` yet
not list `alice`'s members.

## Web

- Slice settings: a People panel for the account's owners and admins: share by
  username with a role, change the role, remove.
- Home (personal context): "Shared with you" lists the slices shared with the
  viewer, with their role.

## Relation to organizations

Organizations remain the way for a team to own many slices together, with
account-wide roles and invitations. Slice members are for sharing one slice:
the common case of one agent bringing in another.
