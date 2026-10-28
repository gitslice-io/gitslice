# 33. Secrets

Status: built 2026-10-27 (masking and the old table's removal the same day).

## Goal

A secret is a named value that Gitslice keeps encrypted and passes to a slice's
CI checks as an environment variable. Each secret used to belong to one slice
(`slice_secrets`, migration 0020), so a token that several slices need had to
be set again on every slice and rotated in every place. Secrets could be
managed only through `gs slice secret` and the `SliceService` RPCs. The web UI
had nothing for them.

Now:

- a secret belongs to an **account** and applies to **one or more of the
  account's slices**, or to all of them;
- `SecretService` manages secrets by id, with their scope;
- the account has a Secrets page, and slice settings have a Secrets panel;
- `gs secret` manages them from the CLI, and `gs slice secret` and the
  `*SliceSecret` RPCs keep working on top of the new model.

How secrets reach checks did not change: `GetSliceSecrets(sliceID)` still
returns the name→value map for one slice, and check dispatch
(`service/check_dispatch.go`) calls it exactly as before.

Decisions, made with the user before building:

1. One kind of secret. Slice secrets were folded into account secrets: a slice
   secret is now an account secret scoped to that one slice. The alternative
   was GitHub's design, with slice secrets beside account secrets. That needs
   no data migration but gives users two kinds of secret that look nearly the
   same.
2. "All slices" is a scope, so a new slice does not have to be added to every
   shared secret by hand.
3. No cross-account scope. A secret reaches only its own account's slices,
   even ones shared with the account under design/26.

## Model

```
secret
  id            sec_…
  account       the account that owns it
  name          ^[A-Z_][A-Z0-9_]*$, the environment variable name; never changes
  value         sealed with secretbox (enc:v1); returned only by RevealSliceSecrets, to account admins
  scope         all_slices, or a set of slices of the account
  created_by / updated_by / created_at / updated_at
```

- **Scope.** A secret applies either to every slice of its account, including
  slices created later (`all_slices`), or to the slices it lists. A listed
  slice must belong to the secret's account.
- **One value per name per slice.** Several secrets in an account may have the
  same name if no slice is listed in more than one of them. So `DEPLOY_TOKEN`
  can hold one value for `api` and `worker` and another for `web`. A write that
  would list a slice in a second same-name secret fails with
  `FailedPrecondition` (`storage.ErrConflict`) and names the secret that
  already covers that slice.
- **Precedence.** For a given name, a secret that lists the slice beats an
  `all_slices` secret. An account can have at most one `all_slices` secret per
  name. An account-wide default plus per-slice overrides therefore works like
  GitHub's org and repo secrets.
- **Effective secrets of a slice** = the secrets that list the slice, plus the
  account's `all_slices` secrets whose names those do not already cover. This
  is what `GetSliceSecrets` returns.
- **Values reach checks, and admins only on request.** No page shows a value.
  The only call that returns one is `RevealSliceSecrets`, and only to the
  owners and admins of the account, the people who can already replace it (see
  "Local env files"). To change a value you replace it.
- **Limits.** A value can be up to 48 KiB, and an account can have up to 200
  secrets (`storage.MaxSecretValueBytes`, `storage.MaxSecretsPerAccount`).
  Names follow `storage.ValidSliceSecretName`.

### Who may do what

Secrets stay with the **account's owners and admins** (`authorizeAccount(...,
ActionAdmin)`, the same check as `authorizeSliceOwner`). The reasoning of
design/26 holds: a slice admin who could point a secret at their slice, or a
slice at their own CI daemon, could receive the value.

| Action | Who |
| --- | --- |
| Create, update value or scope, delete | Account owners and admins |
| `ListSecrets`, `GetSecret` (scope and ids) | Account owners and admins; to anyone else a secret id is `NotFound` |
| `ListSliceSecrets` names | Writers of the slice, as before |
| `ListSliceSecrets` sources (`secrets`) | Account owners and admins |
| `RevealSliceSecrets` (values) | Account owners and admins; refused to slice admins and writers |

Exposure is what users need to understand. A secret reaches every CI daemon
that runs checks for any slice in its scope, and every writer of those slices
can open a changeset whose check prints it. Widening a scope widens both. The
web form shows this next to the scope picker, and `SecretScope`'s proto
comment, `gs help secrets` and the Checks doc say it too.

## Storage

Migration `0045_secrets.sql` adds:

- `secrets(id, account_id, name, value, all_slices, created_by, updated_by,
  created_at, updated_at)`, with a partial unique index on
  `(account_id, name) where all_slices`.
- `secret_slices(secret_id, slice_id, name)`. Both foreign keys cascade on
  delete, and a unique index on `(slice_id, name)` enforces the "one value per
  name per slice" rule. `name` is a copy of the secret's name. Names never
  change, so the copy cannot drift.

Writes lock the account's row (`select … for update`). That way the conflict
checks and the per-account limit see every other write to the account. A
unique-index violation from a race those checks missed is still reported as
`ErrConflict`. The store's code is in `internal/postgres/secrets.go`, and
`internal/storage/memory/secrets.go` mirrors it.

**The old table.** Production had no slice secrets (0 rows, checked
2026-10-27) before secrets moved, so nothing was copied. Migration
`0046_drop_slice_secrets.sql` drops `slice_secrets`, and `0020_slice_secrets.sql`
is gone. (Migrations run at every start, so 0020 would have recreated the
table each time.) An earlier draft of 0045 copied slice secrets once, guarded
by a `secret_migrations` marker table. 0046 drops that table too, for the
development databases that ran the draft.

**Slice lifecycle (design/29).** Deleting a slice cascades its
`secret_slices` rows. Retiring one deletes them explicitly. A secret whose
list becomes empty is kept and shown as "Not used by any slice", so its owners
can rescope or delete it. Renaming a slice changes nothing, because rows refer
to the slice id. Archived slices run no checks, so they receive nothing.

**Encryption.** Values use `secretbox` with `GITSLICE_SECRETS_KEY`, as before.
Possible hardening, not done: `enc:v2`, which would bind a sealed value to its
secret id through GCM associated data. That would stop someone with database
write access from copying a sealed value into a secret that reaches another
slice.

**Interfaces.** The methods live on `storage.SliceStore`, next to the slice
secret methods they replace, because that store already holds the secrets box:

- `CreateSecret`, `UpdateSecret`, `DeleteSecret`, `GetSecret`, `ListSecrets`
- `ListSliceSecrets` (effective secrets of a slice, with their sources)
- `SetSliceSecret` and `DeleteSliceSecret` (these now take the actor)
- `ListSliceSecretNames` and `GetSliceSecrets`

## API

`proto/core/v1/secret.proto`, served over Connect
(`/gitslice.core.v1.SecretService/*`) and gRPC:

```proto
service SecretService {
  rpc ListSecrets(ListSecretsRequest) returns (ListSecretsResponse);   // {account}
  rpc GetSecret(GetSecretRequest) returns (Secret);                    // {id}
  rpc CreateSecret(CreateSecretRequest) returns (Secret);              // {account, name, value, scope}
  rpc UpdateSecret(UpdateSecretRequest) returns (Secret);              // {id, optional value, scope}
  rpc DeleteSecret(DeleteSecretRequest) returns (Empty);               // {id}
}

message Secret {
  string id, account, name;
  SecretScope scope;
  string created_by, updated_by;   // usernames
  string created_at, updated_at;   // RFC 3339
}

message SecretScope {
  bool all_slices = 1;
  repeated SliceRef slices = 2;    // ignored when all_slices; a ref without an
                                   // account means the secret's own account
}
```

In `UpdateSecretRequest`, leaving out `value` or `scope` keeps it. Errors:

- `InvalidArgument`: a bad name, an empty scope, a slice from another account
  or one that does not exist, or a value over 48 KiB.
- `FailedPrecondition`: a name already in use for a slice in the scope, or the
  account at its secret limit.
- `PermissionDenied`: an account non-admin calling `ListSecrets` or
  `CreateSecret`.
- `NotFound`: an id the caller cannot manage.

### Compatibility: the slice secret RPCs

`SetSliceSecret`, `DeleteSliceSecret` and `ListSliceSecrets` keep their
signatures. Older CLIs and scripts keep working.

- `SetSliceSecret(slice, NAME, value)`:
  - A secret that lists **only** this slice and has that name gets the new
    value.
  - A secret that lists **other slices too** is left alone: the call fails
    with `FailedPrecondition` ("NAME on this slice comes from secret sec_…,
    shared with N other slice(s); …"). It never silently changes the value
    other slices receive.
  - Otherwise a new secret is created that lists only this slice. That
    overrides an `all_slices` secret of the same name there.
- `DeleteSliceSecret(slice, NAME)`:
  - Deletes a secret that lists only this slice.
  - Takes this slice out of the list of a secret that lists other slices too.
  - Refuses an `all_slices` secret (`FailedPrecondition`).
  - Does nothing when no secret by that name reaches the slice.
- `ListSliceSecrets(slice)`: `names` lists the names of the secrets that reach
  the slice. A new field, `repeated SliceSecret secrets = 2`, holds
  `{name, secret_id, all_slices, shared_with}` for account admins only.

## CLI

```bash
gs secret ls [--account <account>] [--json]
gs secret create <NAME> [--account <account>] (--slice <slice>... | --all-slices) < value
gs secret update <secret-id> [--value] [--slice <slice>... | --add-slice <slice>... | --remove-slice <slice>... | --all-slices]
gs secret rm <secret-id>
gs slice secret set <slice> <NAME> < value      # value argument still accepted, with a warning
gs slice secret rm|ls <slice>
```

- **Values.** Values are read from stdin, minus one trailing newline, or typed
  hidden when stdin is a terminal. They are never taken as an argument, which
  would leave them in shell history and `ps`. `gs slice secret set` still
  accepts a value argument but prints a warning to stderr.
- **Accounts and slices.** `--account` defaults to the active account. A bare
  `--slice` name means a slice of the secret's account.
- **Help.** `gs help secrets` explains the model.

## Web UI

Only account owners and admins see these screens (`canAdmin`). The server
checks again.

**Account → Secrets** (`/accounts/$account/secrets`,
`routes/AccountSecretsPage.tsx`). The page is reached from a "Secrets" button
on the account page and holds `components/secrets/AccountSecretsPanel.tsx`:

- Each row shows the name, the slices the secret reaches ("All slices", a
  list, or "Not used by any slice" in amber), who changed it last and when,
  and Edit and Delete buttons.
- **New secret** and **Edit** open `SecretForm` inline:
  - Name: read-only when editing.
  - Value: a textarea, masked until "Show" is pressed, so multi-line values
    such as PEM keys fit. When editing it starts empty and keeps the current
    value unless something is typed.
  - Scope: a radio choice between "Selected slices", a checkbox list of the
    account's slices with a filter when there are many, and "All slices". The
    exposure note stays under the scope picker.
  - The server's refusals, such as an overlap, appear in the form.
- **Delete** asks in a `ConfirmDialog` and names the slices whose checks lose
  the secret.

**Slice settings → Secrets panel** (`components/slices/SliceSecretsPanel.tsx`).
It sits between the members and webhooks panels:

- It lists the secrets that reach the slice, with where each comes from:
  "This slice only", "Shared with …", or "All slices of …".
- A secret that only this slice has is edited and deleted here. A shared one
  links to the account page, because changing it changes other slices too.
- "Add secret" opens the form with the scope preset to this slice.
- When the slice has no CI daemon, a notice says its checks receive no secrets
  until one is set.

Docs: a "Pass secrets to checks" section in the Checks doc, a Secrets group in
the CLI reference, and a line in the gs-cli skill.

## Local env files

Deploying the web app needs production credentials in a local `.env.prod`
(`web/scripts/deploy.sh` sources it). The committed `env.prod.template`
lists every variable that file needs. Public values are written out, and each
secret is a placeholder, `NAME=gitslice-secret:SECRET_NAME`. The account's
owners and admins generate the file:

```bash
gs secret env env.prod.template --slice gitslice/gitslice -o .env.prod
```

- **The call.** `SecretService.RevealSliceSecrets(slice, names)` returns the
  values of the named secrets that reach the slice, plus the names that none
  do. It takes 1 to 100 names, and the caller must be an owner or admin of the
  slice's account (`authorizeSliceOwner`).
- **Who is refused.** Slice admins and writers, who receive the values inside
  checks, cannot read them out. That keeps an outside writer from collecting
  credentials with one command and no changeset to show for it.
- **Audit.** Each call logs `secrets revealed` with the subject, the slice and
  the names, never the values.
- **The CLI.**
  - It refuses to print values to a terminal and writes the output file with
    mode 600, replacing it in one step.
  - It won't overwrite an existing file without `--force`.
  - If any secret is missing it writes nothing.
  - Values that are not shell-safe are single-quoted, since the file is
    sourced by bash.
- **Rotation.** `gs secret update <id> --value`, then regenerate the file.

Moving the deploy into a CI job, so that nobody needs production credentials
locally, remains the longer-term answer.

## Masking secrets in check output

A check that prints a secret must not store it in a log that every reader of
the changeset can open. Both ends replace secret values with `***` in what a
check sends back: its log and its summary (`internal/secretmask`).

- **The daemon** masks with the values it was sent. `CheckRunSpec.secret_env`
  names the env entries that hold secrets, so the daemon can tell them from
  the check's own env (`internal/cli/agent_checks.go`). This also covers a
  secret changed while the check ran.
- **The server** masks every log chunk and summary it receives with the
  current secrets of the run's slice (`runSecretMasker` in
  `service/check_dispatch.go`). So daemons from before this change store no
  secrets either. A daemon sends a check's whole log in one update, so no value
  is ever split across two chunks.
- **What is masked.** Each value of at least 4 characters (`secretmask.MinLength`)
  is masked. Shorter ones would blank out ordinary text and hide little. A value
  of several lines is also masked line by line, as a log prints a key file.
- **What is not masked.** Logs stored before this change are not rewritten.
  Masking is no barrier to a writer who sets out to leak a secret, for example
  by encoding it or sending it over the network: as before, writers of a slice
  are trusted with its secrets.

## Testing

- **Postgres store** (`internal/postgres/secrets_test.go`):
  - encryption at rest, legacy plaintext, and plaintext mode;
  - scoping one secret to several slices;
  - overlap conflicts and the all-slices uniqueness rule;
  - precedence in `GetSliceSecrets`;
  - rescoping;
  - rejecting another account's slice;
  - the compatibility behaviour of the slice calls;
  - a deleted slice leaving an empty scope.
- **Service** (`service/secret_test.go`):
  - authorization: an account writer is refused and an outsider gets
    `NotFound`;
  - scope validation;
  - an update that leaves the other field alone;
  - the `ListSliceSecrets` sources, shown to admins only.
- **Connect** (`server/server_test.go`): `SecretService` is mounted.
- **RPC** (`tests/rpc/check_dispatch_test.go`): a secret scoped to two slices
  and an all-slices secret both reach the check env.
- **Masking** (`internal/secretmask`, `internal/cli/agent_checks_mask_test.go`,
  and the dispatch RPC test): specs name the secret env, and a daemon that does
  not mask still has its log and summary masked by the server.
- **CLI** (`tests/cli/secrets_test.go`): `gs secret` create, update, ls and
  rm, together with the slice commands.
- **Reveal** (`service/secret_test.go`, `internal/cli/secret_env_test.go`,
  `tests/cli/secrets_test.go`): values only for account admins, refused to a
  slice admin and an account writer; template parsing and quoting; the
  mode-600 file, `--force`, and nothing written when a secret is missing.
- **Web** (`components/secrets/Secrets.test.tsx`): listing, creating with a
  scope, editing that keeps the value and shows a refusal, deleting, and the
  slice panel's sources and preset scope.

## Rollout

1. Server deploy: migrations 0045 and 0046, `SecretService`, the
   compatibility RPCs, and server-side masking.
2. Web deploy: the account page and the slice panel.
3. The `gs secret` commands and daemon-side masking ship in the next CLI
   release.

## Follow-ups

- Secrets for agent runtimes (design/16), not just checks.
- An audit log of secret changes. For now `updated_by` and `updated_at` are
  the only record.
- `enc:v2` (associated data bound to the secret id) and key rotation for
  `GITSLICE_SECRETS_KEY`.
