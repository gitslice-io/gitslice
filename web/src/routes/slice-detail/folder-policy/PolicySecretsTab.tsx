import { Link } from "@tanstack/react-router";

import type { FolderSecretEntry } from "../../../api/types";
import { SecretForm, type SecretDraft } from "../../../components/secrets/SecretForm";
import { SliceNotice } from "../../../components/slices/SlicePageParts";
import { Button } from "../../../components/ui/Button";
import { InheritanceBadge } from "./PolicyShared";

export interface PolicySecretsTabProps {
  account: string;
  addingSecret: boolean;
  canManageSecrets: boolean;
  folder: string;
  isPending: boolean;
  onCancelAdd(): void;
  onStartAdd(): void;
  onSubmitSecret(draft: SecretDraft): void;
  secretError: string;
  secrets: FolderSecretEntry[];
}

export function PolicySecretsTab({
  account,
  addingSecret,
  canManageSecrets,
  folder,
  isPending,
  onCancelAdd,
  onStartAdd,
  onSubmitSecret,
  secretError,
  secrets
}: PolicySecretsTabProps) {
  return (
    <div className="grid gap-4 px-4 py-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <p className="max-w-md text-xs leading-5 text-slate-600 dark:text-zinc-400">
          Secrets defined on <span className="font-mono">{folder}</span> or inherited from an ancestor folder are
          injected into checks and automations here.
        </p>
        {canManageSecrets && !addingSecret ? (
          <Button onClick={onStartAdd} size="sm" variant="primary">
            Add secret
          </Button>
        ) : null}
      </div>

      {secretError ? (
        <SliceNotice title="Could not save secret" tone="error">
          {secretError}
        </SliceNotice>
      ) : null}

      {canManageSecrets && addingSecret ? (
        <SecretForm
          account={account}
          busy={isPending}
          initial={{ name: "", paths: [folder], value: "" }}
          onCancel={onCancelAdd}
          onSubmit={onSubmitSecret}
          submitLabel="Add secret"
        />
      ) : null}

      {secrets.length === 0 ? (
        <SliceNotice title="No secrets apply here.">
          {canManageSecrets
            ? `Add a secret scoped to ${folder} (or /${account} for the whole monorepo).`
            : "Only writers and admins of this folder see secret names. Values are never shown in the UI."}
        </SliceNotice>
      ) : (
        <ul className="divide-y divide-slate-200 overflow-hidden rounded-md border border-slate-200 dark:divide-zinc-800 dark:border-zinc-800">
          {secrets.map((secret) => (
            <SecretRow folder={folder} key={secret.name} secret={secret} />
          ))}
        </ul>
      )}

      {canManageSecrets && account ? (
        <p className="text-xs text-slate-500 dark:text-zinc-400">
          Manage, rotate, or delete secrets across folders on{" "}
          <Link
            className="font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
            params={{ account }}
            to="/accounts/$account/secrets"
          >
            {account}'s Secrets page
          </Link>
          .
        </p>
      ) : null}
    </div>
  );
}

function SecretRow({ folder, secret }: { folder: string; secret: FolderSecretEntry }) {
  return (
    <li className="flex flex-wrap items-center justify-between gap-2 px-3 py-2.5 text-xs">
      <div className="min-w-0">
        <p className="break-all font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">{secret.name}</p>
        {(secret.paths ?? []).length > 1 ? (
          <p className="mt-0.5 break-all font-mono text-[11px] text-slate-500 dark:text-zinc-400">
            scopes: {(secret.paths ?? []).join(", ")}
          </p>
        ) : null}
      </div>
      {secret.sourceFolder ? <InheritanceBadge currentFolder={folder} sourceFolder={secret.sourceFolder} /> : null}
    </li>
  );
}
