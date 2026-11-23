# 36. Multi-Agent Collaboration on a Slice

Status: implemented (2026-11-17)

## Goal

Several agents, and the people they work for, should be able to work in one
slice at once, each with only the authority its task needs, without
overwriting each other's work, and without one person's agents approving
each other's changes. A person should be able to tell, for any change, which
agent made it, on whose behalf, and with which credential.

## Background: prior access model

Before this design shipped, subjects were people (`kind = user`) and agents
(`kind = agent`, design/12). An agent had its own personal account and home
slice, and the person who claimed it became a co-owner of that account.

Access to a slice was the higher of two roles (`internal/authz`):

| Where | Roles | Grants |
|---|---|---|
| Account membership (design/12) | owner, admin, member, writer, reader | the whole account |
| Slice member (design/12) | admin, writer, reader | one slice |

They collapsed into three actions: `read`, `write`, `admin`.

- **read**: read files, check out, clone.
- **write**: everything that changed code:
  - create changesets, push new patchsets **to any changeset**, edit any
    changeset's title, and submit **any** changeset;
  - approve, report check results, tag, and push with Git;
  - run checks with the account's secrets.
- **admin** (slice): members and settings.
- **Kept for the account's owners and admins**: the slice's definition,
  CI daemon, secrets and deletion; a change under `.gitslice/` needed one of
  them as author or approver (design/34).

Credentials:

- Sessions (from a browser sign-in) and API keys (`gsk_`, design/12) both
  carried the **full authority of their subject**. There were no per-key scopes,
  no expiry by default, and no key limited to a slice.
- A `gs agent` daemon authenticated with the saved `gs` login of the person
  who started it (design/16). The coding agents it ran didn't get `GS_TOKEN`,
  but they kept `HOME`, so on that machine they could use the saved login, with
  all of that person's authority, on every account the person belonged to.

Approvals:

- `required_approvals` counted distinct subjects other than the changeset's
  and patchset's authors (`storage.FilterApprovalSubjects`).

## Problems addressed for multi-agent work

1. **`writer` was all or nothing.** An agent that should only propose
   changes could also submit them, approve others, tag releases, push with
   Git, and run code with the account's secrets. There was no role for an
   untrusted or experimental agent between `reader` and `writer`.
2. **Changesets had no owner.** Any writer could push a patchset onto, submit
   or abandon another agent's changeset. Two agents working on related tasks
   could overwrite each other's work by accident.
3. **Approvals could be gamed by one person's agents.** Agent A wrote, agent
   B approved: two subjects, so a `required_approvals: 1` slice let one
   person land unreviewed code by running two agents. Nothing recorded that
   both acted for the same person, and no rule could ask for a person's review.
4. **Credentials were too broad.** A key or daemon login granted everything
   its subject could do, everywhere, with no expiry. A compromised or
   misbehaving agent in one slice could act on every slice of every account
   its owner belonged to.
5. **No ownership inside a slice.** A slice was one path set with one set of
   writers. The only finer rule was the `.gitslice/` approval. Agents that owned
   different areas (the API, the web app, the infra) couldn't be required to
   review changes to their area.
6. **No coordination.** Agents learned that they edited the same files only at
   submit time, through a conflict.
7. **Audit was per subject only.** A patchset recorded its author subject, not
   which key, daemon or conversation produced it, nor the person the agent
   worked for.

## Design

The pieces are independent and can ship in order. Phase 1 fixes problems
1–3 with small schema changes.

### 1. Roles (phase 1)

Four roles at both levels, account and slice, from least to most:

| Role | New? | Can |
|---|---|---|
| `reader` | | read, check out, clone |
| `contributor` | new | create changesets and push patchsets to changesets it owns (section 2); its checks run on the slice's CI daemon **without secrets** until a writer allows that patchset to use them; cannot submit or approve |
| `writer` | | as today (submit, approve, tag, push with Git, checks with secrets, rerun checks, allow a contributor's patchset to use secrets), but only on changesets it owns (section 2); it may approve anyone's |
| `admin` | | writer, plus slice members, webhooks, and acting on **any** changeset (for one whose owner is gone) |

There is no maintainer role. With writers approving, the only extra power it
had was acting on other people's changesets, which changeset ownership
(section 2) and admins cover.

The account-only powers (definition, CI daemon, secrets, deletion,
`.gitslice/` approval) stay with the account's owners and admins.

No migration: existing roles keep their names. The one change existing
writers see is section 2: they can no longer push to, submit or abandon a
changeset they don't own. Agents added to someone else's slice should
usually start as `contributor`.

`authz.Action` grows from three actions to named capabilities (`read`,
`propose`, `submit`, `review`, `tag`, `use_secrets`, `manage_members`,
`act_on_any_changeset`), and each RPC names the one it needs. That keeps role
changes in one table (`roleAllows`) instead of in every service.

Contributors' checks: a contributor's patchset runs its checks on the
slice's CI daemon with no secrets. Checks that need secrets wait (status
`needs_approval`) until a writer runs `gs check allow <changeset>`. That
covers the patchset, not later ones, like GitHub's approval for pull
requests from forks. It reuses the per-patchset approval behind
`patchsetApprovedForGitsliceSecrets`.

### 2. Changeset ownership (phase 1)

These subjects **own** a changeset, and only they may push a patchset to,
edit, submit or abandon it, each within their role (a contributor still
can't submit):

- its author;
- the author's **controller** (section 3): a person can do anything to their
  agents' changesets that the agent could. They act with the **agent's** role
  on the slice, even when they aren't a member of it themselves, since they
  control the agent and could use its key anyway. The audit records the
  person, not the agent;
- collaborators the owners add (`gs cs collaborators add <cs> <user>`);
- the slice's admins.

Handing work over is explicit: `gs cs handoff <cs> <user>` makes another
subject the author and keeps the original author in the history.
`getChangesetForAction` checks all of this in one place.

### 3. Controllers and approval policy (phase 1)

Every agent gets a **controller**: the person accountable for it.

- an agent registered with an owner email: the person who claims it;
- an agent a signed-in person creates (section 4): that person;
- a person: themselves.

Stored as `subjects.controller_subject_id`, it changes only through a
claim or an explicit transfer.

Approvals then count **controllers**, not subjects. An approval counts only
when the approver's controller differs from the controller of the
changeset's author and of every patchset author. One person's agents can't
approve each other's work, and a person's approval doesn't count on their
own agents' changesets.

An agent no one has claimed has no controller, and its approvals never
count.

Slices get two more submit requirements:

- `required_human_approvals` (default 0): approvals from subjects of kind
  `user`.
- `approvers` (optional, phase 3 with groups): a group whose approvals
  count.

### 4. Scoped, delegated credentials (phase 2)

A key gets an optional **grant**, and its authority is the lesser of the
subject's role and the grant:

```sql
alter table api_keys
  add column slice_ids text[],              -- null: every slice the subject can reach
  add column max_role text,                 -- reader | contributor | writer
  add column parent_key_id text,            -- set for keys minted from another key
  add column purpose text not null default '',         -- e.g. "daemon my-ci", "conversation cv_..."
  add column daemon_id text not null default '',       -- set for daemon-minted keys
  add column conversation_id text not null default ''; -- set for per-conversation child keys
```

- `gs auth keys create --slice acme/payment --role contributor --expires 7d`.
- **Delegated agents.** A signed-in person can create an agent subject that
  they control, with no separate email claim:
  `gs agent create release-bot --slice acme/payment --role writer`. The agent
  is added to the slice with that role, and the person gets its first key.
  This is the "agents acting on behalf of an existing human account"
  case that was originally out of scope when agent signup shipped.
- **Daemon and conversation keys.** A `gs agent` daemon gets its own key,
  limited to the slices it's exposed to. For each **conversation** (note:
  per-check-run child keys are not implemented; child keys are minted for
  conversations only) it mints a child key that expires with the run (at most
  12 h), names that one slice, and has the role the conversation was started
  with (default `contributor`). That key goes into the agent's environment as
  `GS_TOKEN`, and `HOME` points at a run folder with no saved login. The agent
  can then touch only its own slice, with only its role, for only as long as
  the run.
  **Limit:** the agent still runs as the daemon's OS user, so it can read
  the daemon's own saved login by its path (`~/.gitslice/config.json`); a
  separate `HOME` only stops it being used by default. That login should be
  a key limited to the daemon's slices, not the person's full sign-in, and
  for real isolation the daemon runs as its own OS user. `gs agent doctor`
  warns ("agent isolation") when the daemon is signed in with a full
  sign-in.
- A child key can't exceed its parent. Revoking a key revokes its children.

### 5. Path owners inside a slice (phase 3)

`.gitslice/owners.yaml`, read like `merge.yaml` (design/07 §7.5): from the
new base, the nearest file wins, and the change can't change the rules of
its own review.

```yaml
version: 1
owners:
  - paths: ["api/**"]
    owners: ["@acme/api-agents", "alice"]
  - paths: ["web/**"]
    owners: ["web-agent", "bob"]
    require: 1          # approvals from these owners, counted by controller
```

- A changeset that touches owned paths needs approvals from those owners.
  Authors who own every path they touched need none from themselves.
- `.gitslice/**` is implicitly owned by the account's owners and admins,
  which is today's rule.
- **Groups** (`@account/name`, account-level lists of subjects) let several
  agents share one area. Groups can also be slice members.

### 6. Coordination: path intents (phase 3)

Advisory, not enforced:

- `gs intent acme/payment/api/** --for 2h -m "refactor auth"` records that an
  agent is working there.
- `gs status`, `gs sync` and `gs create` warn when the workspace's edits
  overlap another subject's open intent or open changeset, and name them.
- Intents expire and disappear when the changeset that mentions them lands.

This turns "learn about the conflict at submit time" into "see who else is
there before starting". It needs no new permission: anyone who can read the
slice sees its intents.

### 7. Audit

Patchsets, approvals, submits, tags and check allowances record:

- `acting_subject`;
- `controller_subject`;
- `key_id`;
- optional `daemon_id` and `conversation_id`.

The changeset page shows "web-agent (for nic) via my-ci, conversation …".

## API and CLI summary

```bash
gs slice share acme/payment web-agent --role contributor
gs slice update acme/payment --required-human-approvals 1
gs cs collaborators add <cs> api-agent
gs cs handoff <cs> bob
gs check allow <cs>                     # writer: run this contributor patchset's checks with secrets
gs agent create release-bot --slice acme/payment --role writer
gs auth keys create --slice acme/payment --role contributor --expires 7d
gs intent 'acme/payment/api/**' --for 2h -m "refactor auth"
```

## Phasing

1. The contributor role, changeset ownership, controllers, and
   controller-counted approvals with `required_human_approvals`. These are
   mostly `internal/authz`, `getChangesetForAction`, `FilterApprovalSubjects`
   and one migration. They close problems 1–3.
2. Scoped keys, delegated agents, and per-run keys for daemon agents. These
   close problem 4 and the biggest risk: a daemon's agents acting with their
   owner's full login.
3. `owners.yaml`, groups, and path intents (problems 5 and 6).
4. Audit fields come with whichever phase first records each actor.

## Decisions (2026-11-17)

- Writers approve; there is no maintainer role.
- A contributor's changesets run on the slice's CI daemon before anyone
  allows them, without secrets.
- An agent no one has claimed can't approve: its approvals never count.
- A person can act on their agents' changesets with the agent's role.
