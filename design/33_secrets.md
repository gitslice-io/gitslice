# 33. Secrets

Status: built 2026-10-27 with account and slice scopes. Reworked 2026-10-29:
a secret is scoped to **folders** (path prefixes), and slice scopes are gone.

## Goal

A secret is a named value that Gitslice keeps encrypted and passes to CI
checks as an environment variable.

History:

1. Each secret first belonged to one slice (`slice_secrets`, migration 0020),
   so a token that several slices need had to be set again on every slice.
2. On 2026-10-27 secrets moved to the account, scoped to "all slices" or to a
   list of slices (migration 0045).
3. That still left two ideas that look alike (account-wide and slice-scoped),
   and neither fits code that lives in a folder rather than a slice: a check
   is defined in `<dir>/.gitslice/checks.yaml` and runs for any slice whose
   change touches that folder, and the planned triggers (`triggers.yaml`) work
   the same way. So on 2026-10-29 the scope became a set of folders.

Now there is one rule: **a secret applies to the folders it names and
everything below them.**

| Before | Now |
| --- | --- |
| All slices of account `acme` | `/acme` (the account root) |
| Slice `acme/api`, which includes `/acme/api` | `/acme/api` |
| (not possible) | `/acme/services/billing` |

## Model

```
secret
  id            sec_…
  account       the account that owns it; every path is inside /<account>
  name          ^[A-Z_][A-Z0-9_]*$, the environment variable name; never changes
  value         sealed with secretbox (enc:v1); returned only by the reveal calls, to account admins
  paths         1 to 50 canonical folders, such as /acme or /acme/services/billing
  created_by / updated_by / created_at / updated_at
```

- **Paths.** Absolute and canonical, like a slice's included paths
  (`paths.CanonicalPrefix`): `/acme/services/billing`, no trailing slash. The
  first segment must be the secret's account, so a secret never reaches
  another account's code. The account root (`/acme`) is allowed. A folder does
  not have to exist yet.
- **Where a secret applies.** At folder `F`, the secrets that apply are those
  with a path that is `F` or an ancestor of `F`.
- **Precedence.** For each name, the secret whose path is the **longest**
  prefix of `F` wins. So `DEPLOY_TOKEN` at `/acme` is a default and
  `DEPLOY_TOKEN` at `/acme/web` overrides it under `/acme/web`. This replaces
  "a slice-listed secret beats an all-slices one".
- **One secret per name per path.** Two secrets with the same name may not
  list the same path (`FailedPrecondition`, naming the secret that has it).
  Nested paths are fine: that is how overrides work.
- **Values reach checks, and admins only on request.** No page shows a value.
  Only `RevealSecrets` and `RevealSliceSecrets` return values, and only to the
  account's owners and admins, the people who can already replace them.
- **Limits.** A value can be up to 48 KiB, a secret up to 50 paths, and an
  account up to 200 secrets (`storage.MaxSecretValueBytes`,
  `storage.MaxSecretPaths`, `storage.MaxSecretsPerAccount`).

### Who uses a secret, and at which folder

| Consumer | Folder it asks for |
| --- | --- |
| A CI check | The folder of the `checks.yaml` that defines it: the directory part of the check id (`web/build` → `/<account>/web`; a root check `test` → `/<account>`) |
| Masking a check's output | The same folder as the check |
| `gs secret env` | The folder that holds the template, or `--path` |
| `RevealSliceSecrets` (older CLIs) | The slice's included paths (see Compatibility) |
| Triggers (planned) | The folder of the `triggers.yaml` |

A check therefore gets the same secrets whichever slice's change ran it. Before,
it got the secrets of the changeset's slice.

### Who can read a secret

Secrets stay with the **account's owners and admins** (`authorizeAccount(...,
ActionAdmin)`).

| Action | Who |
| --- | --- |
| Create, update value or paths, delete | Account owners and admins |
| `ListSecrets`, `GetSecret` (paths and ids) | Account owners and admins; to anyone else a secret id is `NotFound` |
| `ListSliceSecrets` names | Writers of the slice |
| `ListSliceSecrets` sources (`secrets`) | Account owners and admins |
| `RevealSecrets`, `RevealSliceSecrets` (values) | Account owners and admins; refused to slice admins and writers |

Exposure is what users need to understand. A secret at path `P` reaches:

- the CI daemon of every slice whose changes run a check defined at or below
  `P`;
- every writer who can land, or open a changeset with, a `checks.yaml` at or
  below `P`: the writers of every slice that includes `P`, a folder above it,
  or a folder inside it. Such a writer can add a check that prints the value.

This is wider than a slice scope was: a writer of a slice that includes only
`/acme/services/billing` can read a secret at `/acme/services`. The account's
admins still decide all of it, because only they create slices, change their
included paths and choose their CI daemons. The web form, `gs help secrets`,
the Secrets doc and `SecretScope`'s proto comment say this next to the path
picker. Approval rules for `.gitslice/` folders, so that only approved people
decide which code receives a secret, are a follow-up (they matter even more
for triggers).

## Storage

Migration `0047_secret_paths.sql` adds `secret_paths(secret_id, path, name)`:

- `secret_id` references `secrets` and cascades on delete;
- primary key `(secret_id, path)`;
- a unique index on `(path, name)` enforces "one secret per name per path".
  `name` is a copy of the secret's name; names never change, so the copy cannot
  drift.

It also converts the old scopes. Migrations run at every start, so each insert
only touches secrets that have **no path yet**: those are exactly the secrets
written before this change, because a secret written since always has one.

- An `all_slices` secret gets its account root, `/<account>`.
- A slice-listed secret gets the included paths of each slice it listed.

If two old secrets of the same name would get the same path (two slices
including the same folder), the second is skipped (`on conflict do nothing`)
and the secret keeps whatever paths remain. Production had 4 secrets, all
scoped to `gitslice/gitslice`, which become `/gitslice/gitslice`.

`secrets.all_slices` and `secret_slices` are **kept, unused**, so the previous
server revision still works if a deploy is rolled back. New code ignores
them, and a rescope sets `all_slices` to false. A later migration drops them.

Lookups:

- **Values at a folder** (`SecretValuesAt`): one query over `secret_paths`
  with `path = any(<the folder and its ancestors>)`, keeping for each name the
  row with the longest path (`distinct on (name) … order by name,
  length(path) desc`).
- **Secrets of a slice** (`ListSliceSecrets`): the account's secrets (at most
  200) filtered in Go to those with a path that covers, or lies inside, one of
  the slice's included paths.

Writes lock the account's row (`select … for update`), so the conflict check
and the per-account limit see every other write to the account; a unique-index
violation from a race is still reported as `ErrConflict`. The code is in
`internal/postgres/secrets.go`, mirrored by `internal/storage/memory/secrets.go`.
Path rules live in one place, `storage.NormalizeSecretPaths` and
`storage.SecretAppliesAt`.

**Slice lifecycle (design/29).** Slices no longer own secrets, so deleting,
retiring or renaming a slice changes no secret. A secret on a folder that no
slice includes any more is kept; its owners can rescope or delete it.

**Encryption.** Values use `secretbox` with `GITSLICE_SECRETS_KEY`, as before.
Possible hardening, not done: `enc:v2`, which would bind a sealed value to its
secret id through GCM associated data.

**Interfaces** (`storage.SliceStore`, which holds the secrets box):

- `CreateSecret(account, name, value, paths)`,
  `UpdateSecret(id, value?, paths?)`, `DeleteSecret`, `GetSecret`,
  `ListSecrets(account)`
- `SecretValuesAt(path)`: the decrypted values that apply at a folder
- `ListSliceSecrets(slice)`: the secrets that apply in a slice, with their paths
- `SetSliceSecret` and `DeleteSliceSecret` (compatibility, below)

`GetSliceSecrets` and `ListSliceSecretNames` are gone: every consumer now asks
for a folder.

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
  rpc RevealSecrets(RevealSecretsRequest) returns (RevealSecretsResponse);                  // {path, names}
  rpc RevealSliceSecrets(RevealSliceSecretsRequest) returns (RevealSliceSecretsResponse);   // {slice, names}
}

message SecretScope {
  repeated string paths = 3;       // what a secret is scoped to; the only field the server returns
  bool all_slices = 1;             // deprecated input: the account root
  repeated SliceRef slices = 2;    // deprecated input: each slice's included paths
}
```

- **Writes.** The server turns a scope into paths: `paths` as given,
  `all_slices` as `/<account>`, and each slice in `slices` as its included
  paths, all merged. Older CLIs (`--slice`, `--all-slices`) therefore keep
  working. Replies carry only `paths`.
- **`RevealSecrets(path, names)`** returns the values of the named secrets that
  apply at `path`, plus the names none does. The caller must be an owner or
  admin of the path's account. 1 to 100 names.

In `UpdateSecretRequest`, leaving out `value` or `scope` keeps it. Errors:

- `InvalidArgument`: a bad name, an empty scope, a relative path, a path
  outside the account or not canonical, more than 50 paths, a slice that does
  not exist, or a value over 48 KiB.
- `FailedPrecondition`: a path that already has a secret by that name, or the
  account at its secret limit.
- `PermissionDenied`: an account non-admin calling `ListSecrets`,
  `CreateSecret` or `RevealSecrets`.
- `NotFound`: an id the caller cannot manage.

### Compatibility

The slice RPCs keep their signatures, so released CLIs keep working. "The
slice's paths" below means its included paths.

- `SetSliceSecret(slice, NAME, value)`:
  - A secret named NAME whose paths are all among the slice's paths gets the
    new value.
  - A secret named NAME on one of the slice's paths that also has **other**
    paths is left alone: `FailedPrecondition` ("NAME comes from secret sec_…,
    which also applies to …"). It never silently changes what other folders
    receive.
  - Otherwise a new secret is created on the slice's paths. That overrides a
    secret of the same name on an ancestor folder.
- `DeleteSliceSecret(slice, NAME)`:
  - Deletes a secret named NAME whose paths are all among the slice's paths.
  - Removes the slice's paths from one that has other paths too.
  - Refuses when NAME only comes from an ancestor folder, such as the account
    root (`FailedPrecondition`, naming the secret).
  - Does nothing when no secret by that name applies.
- `ListSliceSecrets(slice)`: `names` lists the names of the secrets that apply
  anywhere in the slice. For account admins, `secrets` holds
  `{name, secret_id, paths}`; `all_slices` and `shared_with` are no longer
  filled.
- `RevealSliceSecrets(slice, names)`: values at the slice's paths. A slice with
  several included paths where a name has different values fails with
  `FailedPrecondition`, asking for `RevealSecrets` with a path.

## CLI

```bash
gs secret ls [--account <account>] [--json]
gs secret create <NAME> [--account <account>] (--path <folder>... | --slice <slice>...) < value
gs secret update <secret-id> [--value] [--path <folder>... | --add-path <folder>... | --remove-path <folder>... | --slice <slice>...]
gs secret rm <secret-id>
gs secret env <template> [--path <folder> | --slice <slice>] -o <file> [--force]
gs slice secret set|rm|ls <slice> ...
```

- **Paths.** `--path` takes an absolute folder (`/acme/services/billing`;
  `/acme` is the whole account). A relative one is refused rather than
  guessed: in account `gitslice`, `gitslice/web` could mean `/gitslice/web` or
  `/gitslice/gitslice/web`. The API refuses relative paths too. `--slice` is a
  shortcut for the slice's included paths. `--all-slices` is kept hidden, as
  the account root.
- **Values.** Read from stdin, minus one trailing newline, or typed hidden when
  stdin is a terminal; never taken as an argument.
- **`gs secret env`.** Without flags, it asks for the folder that holds the
  template, worked out from the workspace (`gitslice/gitslice/env.prod.template`
  in a workspace → `/gitslice/gitslice`), through `RevealSecrets`. `--slice`
  uses `RevealSliceSecrets`.
- **Help.** `gs help secrets` explains folders, precedence and exposure.

## Web UI

Only account owners and admins see these screens (`canAdmin`); the server
checks again.

**Account → Secrets** (`/accounts/$account/secrets`,
`components/secrets/AccountSecretsPanel.tsx`):

- Each row shows the name, its folders, who changed it last and when, and Edit
  and Delete buttons.
- **New secret** and **Edit** open `SecretForm`:
  - Name: read-only when editing.
  - Value: a textarea, masked until "Show"; when editing it keeps the current
    value unless something is typed.
  - Folders: the chosen paths, each with a remove button; a field to type
    one; and buttons that add the account root or a slice's folders (a list
    instead, when the account has many slices). The exposure note sits under
    it.
  - The server's refusals, such as a path that already has the name, appear in
    the form.
- **Delete** asks in a `ConfirmDialog` and names the folders that lose it.

**Slice settings → Secrets panel** (`components/slices/SliceSecretsPanel.tsx`):

- It lists the secrets that apply in the slice, each with its folders.
- "Add secret" opens the form with the slice's folders filled in.
- A secret whose folders are all the slice's own is edited and deleted here.
  Any other links to the account's Secrets page, because changing it changes
  other folders too.
- When the slice has no CI daemon, a notice says its checks receive no secrets
  until one is set.

Docs: https://gitslice.io/doc/secrets (`web/src/routes/doc/SecretsDoc.tsx`),
the pointer in the Checks doc, `llms.txt` and the gs-cli and deployment
skills.

## Local env files

Deploying the web app needs production credentials in a local `.env.prod`.
The committed `env.prod.template` lists every variable it needs, with secrets
as placeholders, `NAME=gitslice-secret:SECRET_NAME`. The account's owners and
admins generate the file from a workspace:

```bash
gs secret env gitslice/gitslice/env.prod.template -o .env.prod
```

The template sits in `/gitslice/gitslice`, so the secrets that apply there fill
it. The rules from 2026-10-28 still hold: values only to account admins, each
call logged (`secrets revealed`, with the subject, the path and the names),
never printed to a terminal, the file written with mode 600, nothing written
when a secret is missing, and `--force` to replace a file.

## Masking secrets in check output

Both ends replace secret values with `***` in a check's log and summary
(`internal/secretmask`).

- **The daemon** masks with the values it was sent (`CheckRunSpec.secret_env`
  names them).
- **The server** masks every log chunk and summary with the secrets that apply
  at the check's folder (`runSecretMasker`), so older daemons store no secrets
  either.
- Values shorter than 4 characters are not masked; multi-line values are also
  masked line by line. Masking is no barrier to a writer who sets out to leak
  a secret: writers are trusted with the secrets of the folders they can
  write checks in.

## Testing

- **Postgres and memory stores** (`internal/postgres/secrets_test.go`,
  `internal/storage/memory`): encryption at rest and plaintext modes; several
  paths; path validation (outside the account, not canonical, too many);
  same-path conflicts; longest-prefix precedence in `SecretValuesAt`;
  rescoping; the slice compatibility calls; the migration's conversion of
  `all_slices` and slice-listed secrets.
- **Service** (`service/secret_test.go`): authorization; scope conversion
  (`all_slices`, `slices`, `paths`); `RevealSecrets` and
  `RevealSliceSecrets`; `ListSliceSecrets` sources for admins only.
- **RPC** (`tests/rpc/check_dispatch_test.go`): a root check and a nested check
  get the secrets of their own folders, with a nested override; masking uses
  the check's folder.
- **CLI** (`tests/cli/secrets_test.go`, `internal/cli/secret_env_test.go`):
  `--path`, relative paths, `--slice`, `ls`, and `gs secret env` finding the
  template's folder.
- **Web** (`components/secrets/Secrets.test.tsx`): listing folders, creating
  with folders, the slice panel's preset folders.

## Rollout

1. Server deploy: migration 0047, the path-based store, `RevealSecrets`, the
   per-folder lookups in check dispatch and masking. Deploy it right after the
   mirror export, because the web app deploys itself from `main`; the new web
   app also reads the old scope fields, so it still lists secrets correctly
   against the previous server for those minutes.
2. Web: the account page and slice panel with folders (automatic).
3. The `gs secret --path` flags and `gs secret env` by folder ship in the next
   CLI release. Released CLIs keep working through the compatibility rules.

## Follow-ups

- Drop `secrets.all_slices` and `secret_slices` once rollback past this change
  is no longer needed.
- Approval rules for `.gitslice/` folders (who may change which code receives
  a secret).
- Triggers (`triggers.yaml`), which will use the same folder lookup.
- Secrets for agent runtimes (design/16), at the slice's included paths.
- An audit log of secret changes, `enc:v2` and key rotation.
