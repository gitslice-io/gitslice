import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import type { Secret } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { ConfirmDialog } from "../ConfirmDialog";
import { Timestamp } from "../Timestamp";
import { SliceLoadingBlock, SliceNotice, SlicePanel, getErrorMessage } from "../slices/SlicePageParts";
import {
  SecretForm,
  draftFromSecret,
  scopeFromDraft,
  scopeLabel,
  secretButtonClass,
  secretDangerButtonClass,
  secretPrimaryButtonClass,
  type SecretDraft
} from "./SecretForm";

const emptyDraft: SecretDraft = { allSlices: false, name: "", slices: [], value: "" };

export function secretsQueryKey(account: string) {
  return ["secrets", account];
}

// An account's secrets, for its owners and admins: each one's name, the
// slices it reaches and who changed it last. Values are never shown.
export function AccountSecretsPanel({ account }: { account: string }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const queryKey = secretsQueryKey(account);
  const [editing, setEditing] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<Secret | null>(null);
  const [error, setError] = useState("");

  const secretsQuery = useQuery({
    queryKey,
    queryFn: async () => (await api.listSecrets({ account })).secrets ?? []
  });
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey }),
      queryClient.invalidateQueries({ queryKey: ["sliceSecrets"] })
    ]);

  const create = useMutation({
    mutationFn: (draft: SecretDraft) =>
      api.createSecret({ account, name: draft.name, scope: scopeFromDraft(account, draft), value: draft.value }),
    onError: (err) => setError(getErrorMessage(err)),
    onMutate: () => setError(""),
    onSuccess: async () => {
      setEditing(null);
      await refresh();
    }
  });
  const update = useMutation({
    mutationFn: ({ id, draft }: { id: string; draft: SecretDraft }) =>
      api.updateSecret({ id, scope: scopeFromDraft(account, draft), value: draft.value === "" ? undefined : draft.value }),
    onError: (err) => setError(getErrorMessage(err)),
    onMutate: () => setError(""),
    onSuccess: async () => {
      setEditing(null);
      await refresh();
    }
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteSecret({ id }),
    onError: (err) => setError(getErrorMessage(err)),
    onMutate: () => setError(""),
    onSuccess: async () => {
      setDeleting(null);
      await refresh();
    }
  });

  const secrets = secretsQuery.data ?? [];
  const busy = create.isPending || update.isPending || remove.isPending;
  const startEditing = (id: string | null) => {
    setError("");
    setEditing(id);
  };

  return (
    <SlicePanel>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 max-w-2xl">
          <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Secrets</h2>
          <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            Secrets are passed to CI checks as environment variables, in the slices each one is scoped to. Anyone who
            can write to a slice can read the secrets that reach it from a check.
          </p>
        </div>
        {editing !== "new" ? (
          <button
            className={secretPrimaryButtonClass}
            disabled={busy || secretsQuery.isError}
            onClick={() => startEditing("new")}
            type="button"
          >
            New secret
          </button>
        ) : null}
      </div>

      {editing === "new" ? (
        <SecretForm
          account={account}
          busy={create.isPending}
          initial={emptyDraft}
          onCancel={() => startEditing(null)}
          onSubmit={(draft) => create.mutate(draft)}
          problem={error}
          submitLabel="Add secret"
        />
      ) : error && !editing ? (
        <div className="mt-4">
          <SliceNotice title="Secret not changed" tone="error">
            {error}
          </SliceNotice>
        </div>
      ) : null}

      <div className="mt-4">
        {secretsQuery.isPending ? (
          <SliceLoadingBlock />
        ) : secretsQuery.isError ? (
          <SliceNotice title="Could not load secrets" tone="error">
            {getErrorMessage(secretsQuery.error)}
          </SliceNotice>
        ) : secrets.length === 0 ? (
          <p className="text-sm text-slate-500 dark:text-zinc-400">No secrets yet.</p>
        ) : (
          <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 dark:divide-zinc-800 dark:border-zinc-800">
            {secrets.map((secret) => {
              const id = secret.id ?? "";
              if (editing === id) {
                return (
                  <li className="px-4 py-3" key={id}>
                    <SecretForm
                      account={account}
                      busy={update.isPending}
                      editing
                      initial={draftFromSecret(secret)}
                      onCancel={() => startEditing(null)}
                      onSubmit={(draft) => update.mutate({ draft, id })}
                      problem={error}
                      submitLabel="Save secret"
                    />
                  </li>
                );
              }
              const unused = !secret.scope?.allSlices && (secret.scope?.slices ?? []).length === 0;
              return (
                <li className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3" key={id}>
                  <div className="min-w-0 flex-1">
                    <code className="block truncate font-mono text-sm font-medium text-zinc-950 dark:text-zinc-50">
                      {secret.name}
                    </code>
                    <span
                      className={cn(
                        "block text-xs",
                        unused ? "text-amber-700 dark:text-amber-300" : "text-slate-500 dark:text-zinc-400"
                      )}
                    >
                      {scopeLabel(secret.scope)}
                    </span>
                  </div>
                  <span className="text-xs text-slate-500 dark:text-zinc-400">
                    {secret.updatedBy ? `${secret.updatedBy} · ` : "updated "}
                    <Timestamp relative value={secret.updatedAt} />
                  </span>
                  <div className="flex gap-1.5">
                    <button className={secretButtonClass} disabled={busy} onClick={() => startEditing(id)} type="button">
                      Edit
                    </button>
                    <button className={secretDangerButtonClass} disabled={busy} onClick={() => setDeleting(secret)} type="button">
                      Delete
                    </button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      <ConfirmDialog
        busy={remove.isPending}
        confirmLabel="Delete secret"
        message={
          deleting ? <DeleteMessage account={account} secret={deleting} /> : null
        }
        onCancel={() => setDeleting(null)}
        onConfirm={() => deleting?.id && remove.mutate(deleting.id)}
        open={deleting !== null}
        title={`Delete ${deleting?.name ?? "secret"}?`}
        tone="danger"
      />
    </SlicePanel>
  );
}

function DeleteMessage({ account, secret }: { account: string; secret: Secret }) {
  const slices = (secret.scope?.slices ?? []).map((ref) => ref.slice).filter(Boolean);
  const where = secret.scope?.allSlices
    ? `every slice of ${account}`
    : slices.length > 0
      ? slices.join(", ")
      : "";
  return (
    <>
      {where ? (
        <>
          Checks in {where} will no longer get <code className="font-mono">{secret.name}</code>.{" "}
        </>
      ) : null}
      This cannot be undone.
    </>
  );
}
