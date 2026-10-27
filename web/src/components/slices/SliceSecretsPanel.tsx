import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import type { SliceRef, SliceSecret } from "../../api/types";
import { useApi } from "../../api/useApi";
import { ConfirmDialog } from "../ConfirmDialog";
import { secretsQueryKey } from "../secrets/AccountSecretsPanel";
import {
  SecretForm,
  scopeFromDraft,
  secretButtonClass,
  secretDangerButtonClass,
  secretPrimaryButtonClass,
  type SecretDraft
} from "../secrets/SecretForm";
import { SliceLoadingBlock, SliceNotice, SlicePanel, getErrorMessage } from "./SlicePageParts";

// The secrets that reach this slice's CI checks, and where each comes from,
// for the account's owners and admins. A secret only this slice has is
// changed here; a shared one on the account's Secrets page, since changing it
// changes other slices too.
export function SliceSecretsPanel({ hasCIDaemon, slice }: { hasCIDaemon: boolean; slice: SliceRef }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const account = slice.account ?? "";
  const sliceName = slice.slice ?? "";
  const queryKey = ["sliceSecrets", account, sliceName];
  const [editing, setEditing] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<SliceSecret | null>(null);
  const [error, setError] = useState("");

  const secretsQuery = useQuery({
    queryKey,
    queryFn: async () => (await api.listSliceSecrets({ slice })).secrets ?? []
  });
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey }),
      queryClient.invalidateQueries({ queryKey: secretsQueryKey(account) })
    ]);
  const done = async () => {
    setEditing(null);
    setDeleting(null);
    await refresh();
  };
  const onError = (err: unknown) => setError(getErrorMessage(err));

  const create = useMutation({
    mutationFn: (draft: SecretDraft) =>
      api.createSecret({ account, name: draft.name, scope: scopeFromDraft(account, draft), value: draft.value }),
    onError,
    onMutate: () => setError(""),
    onSuccess: done
  });
  const update = useMutation({
    mutationFn: ({ id, draft }: { id: string; draft: SecretDraft }) =>
      api.updateSecret({ id, scope: scopeFromDraft(account, draft), value: draft.value === "" ? undefined : draft.value }),
    onError,
    onMutate: () => setError(""),
    onSuccess: done
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteSecret({ id }),
    onError,
    onMutate: () => setError(""),
    onSuccess: done
  });

  const secrets = secretsQuery.data ?? [];
  const busy = create.isPending || update.isPending || remove.isPending;
  const startEditing = (id: string | null) => {
    setError("");
    setEditing(id);
  };
  const thisSliceOnly: SecretDraft = { allSlices: false, name: "", slices: [sliceName], value: "" };

  return (
    <SlicePanel>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 max-w-2xl">
          <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Secrets</h2>
          <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            Passed to this slice's CI checks as environment variables. Anyone who can write to the slice can read
            them from a check.{" "}
            <Link
              className="font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
              params={{ account }}
              to="/accounts/$account/secrets"
            >
              All of {account}'s secrets
            </Link>
          </p>
        </div>
        {editing !== "new" ? (
          <button
            className={secretPrimaryButtonClass}
            disabled={busy || secretsQuery.isError}
            onClick={() => startEditing("new")}
            type="button"
          >
            Add secret
          </button>
        ) : null}
      </div>

      {!hasCIDaemon ? (
        <div className="mt-4">
          <SliceNotice title="No CI daemon">
            This slice has no CI daemon, so no checks receive its secrets until one is set.
          </SliceNotice>
        </div>
      ) : null}

      {editing === "new" ? (
        <SecretForm
          account={account}
          busy={create.isPending}
          initial={thisSliceOnly}
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
          <p className="text-sm text-slate-500 dark:text-zinc-400">No secrets reach this slice.</p>
        ) : (
          <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 dark:divide-zinc-800 dark:border-zinc-800">
            {secrets.map((secret) => {
              const id = secret.secretId ?? "";
              const own = !secret.allSlices && (secret.sharedWith ?? []).length === 0;
              if (editing === id) {
                return (
                  <li className="px-4 py-3" key={id}>
                    <SecretForm
                      account={account}
                      busy={update.isPending}
                      editing
                      initial={{ ...thisSliceOnly, name: secret.name ?? "" }}
                      onCancel={() => startEditing(null)}
                      onSubmit={(draft) => update.mutate({ draft, id })}
                      problem={error}
                      submitLabel="Save secret"
                    />
                  </li>
                );
              }
              return (
                <li className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3" key={id}>
                  <div className="min-w-0 flex-1">
                    <code className="block truncate font-mono text-sm font-medium text-zinc-950 dark:text-zinc-50">
                      {secret.name}
                    </code>
                    <span className="block text-xs text-slate-500 dark:text-zinc-400">{sourceLabel(account, secret)}</span>
                  </div>
                  {own ? (
                    <div className="flex gap-1.5">
                      <button className={secretButtonClass} disabled={busy} onClick={() => startEditing(id)} type="button">
                        Edit
                      </button>
                      <button className={secretDangerButtonClass} disabled={busy} onClick={() => setDeleting(secret)} type="button">
                        Delete
                      </button>
                    </div>
                  ) : (
                    <Link
                      className="text-xs font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                      params={{ account }}
                      to="/accounts/$account/secrets"
                    >
                      Edit on {account}'s secrets
                    </Link>
                  )}
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
          deleting ? (
            <>
              This slice's checks will no longer get <code className="font-mono">{deleting.name}</code>. This cannot be
              undone.
            </>
          ) : null
        }
        onCancel={() => setDeleting(null)}
        onConfirm={() => deleting?.secretId && remove.mutate(deleting.secretId)}
        open={deleting !== null}
        title={`Delete ${deleting?.name ?? "secret"}?`}
        tone="danger"
      />
    </SlicePanel>
  );
}

function sourceLabel(account: string, secret: SliceSecret) {
  if (secret.allSlices) return `All slices of ${account}`;
  const others = (secret.sharedWith ?? []).map((ref) => ref.slice).filter(Boolean);
  return others.length === 0 ? "This slice only" : `Shared with ${others.join(", ")}`;
}
