import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import type { Secret } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import {
  accountSecretsQuery,
  secretsKey,
  sliceSecretsKeyPrefix
} from "../../queries/secrets";
import { ActionMenu } from "../source/ActionMenu";
import { ConfirmDialog } from "../ConfirmDialog";
import { Timestamp } from "../Timestamp";
import { SliceLoadingBlock, SliceNotice, SlicePanel, getErrorMessage } from "../slices/SlicePageParts";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { useCrudPanelState } from "../ui/useCrudPanelState";
import {
  SecretForm,
  draftFromSecret,
  scopeFromDraft,
  scopeLabel,
  secretPaths,
  type SecretDraft
} from "./SecretForm";

const emptyDraft: SecretDraft = { name: "", paths: [], value: "" };

export const secretsQueryKey = secretsKey;

// An account's secrets, for its owners and admins: each one's name, the
// folders it applies to and who changed it last. Values are never shown.
export function AccountSecretsPanel({ account }: { account: string }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const queryKey = secretsKey(account);
  const crud = useCrudPanelState<Secret>();

  const secretsQuery = useQuery(accountSecretsQuery(api, account));
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey }),
      queryClient.invalidateQueries({ queryKey: sliceSecretsKeyPrefix })
    ]);

  const create = useMutation({
    mutationFn: (draft: SecretDraft) =>
      api.createSecret({ account, name: draft.name, scope: scopeFromDraft(draft), value: draft.value }),
    onError: crud.handleError,
    onMutate: crud.clearError,
    onSuccess: async () => {
      crud.completeAction();
      await refresh();
    }
  });
  const update = useMutation({
    mutationFn: ({ id, draft }: { id: string; draft: SecretDraft }) =>
      api.updateSecret({ id, scope: scopeFromDraft(draft), value: draft.value === "" ? undefined : draft.value }),
    onError: crud.handleError,
    onMutate: crud.clearError,
    onSuccess: async () => {
      crud.completeAction();
      await refresh();
    }
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteSecret({ id }),
    onError: crud.handleError,
    onMutate: crud.clearError,
    onSuccess: async () => {
      crud.completeAction();
      await refresh();
    }
  });

  const secrets = secretsQuery.data ?? [];
  const [filter, setFilter] = useState("");
  const groups = groupSecrets(account, secrets, filter);
  const busy = create.isPending || update.isPending || remove.isPending;

  return (
    <SlicePanel>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 max-w-2xl">
          <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Secrets</h2>
          <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            Secrets are passed as environment variables to the CI checks defined in the folders each one applies to,
            and below them. Anyone who can add a check in a folder can read its secrets from that check.
          </p>
        </div>
        {crud.editing !== "new" ? (
          <Button
            disabled={busy || secretsQuery.isError}
            onClick={crud.startNew}
            size="md"
            variant="primary"
          >
            New secret
          </Button>
        ) : null}
      </div>

      {crud.editing === "new" ? (
        <SecretForm
          account={account}
          busy={create.isPending}
          initial={emptyDraft}
          onCancel={crud.cancelEditing}
          onSubmit={(draft) => create.mutate(draft)}
          problem={crud.error}
          submitLabel="Add secret"
        />
      ) : crud.error && !crud.editing ? (
        <div className="mt-4">
          <SliceNotice title="Secret not changed" tone="error">
            {crud.error}
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
          <>
            {secrets.length > FILTER_FROM ? (
              <Input
                aria-label="Filter secrets"
                className="mb-3 w-full"
                onChange={(event) => setFilter(event.target.value)}
                placeholder="Filter by name or folder"
                type="search"
                value={filter}
              />
            ) : null}
            {groups.length === 0 ? (
              <p className="text-sm text-slate-500 dark:text-zinc-400">No secret matches.</p>
            ) : (
              <div className="grid gap-4">
                {groups.map((group) => (
                  <section
                    aria-label={group.label}
                    className="overflow-hidden rounded-lg border border-slate-200 dark:border-zinc-800"
                    key={group.label}
                  >
                    <div className="flex items-baseline justify-between gap-3 border-b border-slate-200 bg-slate-50 px-4 py-2 dark:border-zinc-800 dark:bg-zinc-950">
                      <h3
                        className={cn(
                          "min-w-0 break-all font-mono text-xs font-semibold",
                          group.unused ? "text-amber-700 dark:text-amber-300" : "text-slate-700 dark:text-zinc-300"
                        )}
                      >
                        {group.label}
                      </h3>
                      <span className="shrink-0 text-xs text-slate-500 dark:text-zinc-400">
                        {group.secrets.length === 1 ? "1 secret" : `${group.secrets.length} secrets`}
                      </span>
                    </div>
                    <ul className="divide-y divide-slate-200 dark:divide-zinc-800">
                      {group.secrets.map((secret) => {
                        const id = secret.id ?? "";
                        if (crud.editing === id) {
                          return (
                            <li className="px-4 py-3" key={id}>
                              <SecretForm
                                account={account}
                                busy={update.isPending}
                                editing
                                initial={draftFromSecret(account, secret)}
                                onCancel={crud.cancelEditing}
                                onSubmit={(draft) => update.mutate({ draft, id })}
                                problem={crud.error}
                                submitLabel="Save secret"
                              />
                            </li>
                          );
                        }
                        return (
                          <li className="flex items-start gap-3 px-4 py-3" key={id}>
                            <div className="min-w-0 flex-1">
                              <code className="block break-all font-mono text-sm font-medium text-zinc-950 dark:text-zinc-50">
                                {secret.name}
                              </code>
                              <span className="mt-0.5 block text-xs text-slate-500 dark:text-zinc-400">
                                Changed <Timestamp relative value={secret.updatedAt} />
                                {secret.updatedBy ? ` by ${secret.updatedBy}` : ""}
                              </span>
                            </div>
                            {/* Buttons on wider screens; a menu on phones, so the name keeps the row. */}
                            <div className="hidden shrink-0 gap-1.5 sm:flex">
                              <Button disabled={busy} onClick={() => crud.startEditing(id)}>
                                Edit
                              </Button>
                              <Button disabled={busy} onClick={() => crud.requestDelete(secret)} variant="danger">
                                Delete
                              </Button>
                            </div>
                            <div className="shrink-0 sm:hidden">
                              <ActionMenu
                                items={[
                                  { label: "Edit", disabled: busy, onSelect: () => crud.startEditing(id) },
                                  { label: "Delete", disabled: busy, onSelect: () => crud.requestDelete(secret), tone: "danger" }
                                ]}
                                label={`Actions for ${secret.name}`}
                              />
                            </div>
                          </li>
                        );
                      })}
                    </ul>
                  </section>
                ))}
              </div>
            )}
          </>
        )}
      </div>

      <ConfirmDialog
        busy={remove.isPending}
        confirmLabel="Delete secret"
        message={
          crud.deleting ? <DeleteMessage account={account} secret={crud.deleting} /> : null
        }
        onCancel={crud.cancelDelete}
        onConfirm={() => crud.deleting?.id && remove.mutate(crud.deleting.id)}
        open={crud.deleting !== null}
        title={`Delete ${crud.deleting?.name ?? "secret"}?`}
        tone="danger"
      />
    </SlicePanel>
  );
}

function DeleteMessage({ account, secret }: { account: string; secret: Secret }) {
  const where = secretPaths(account, secret.scope).join(", ");
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

// Lists of more secrets than this get a filter.
const FILTER_FROM = 8;

export interface SecretGroup {
  label: string;
  unused: boolean;
  secrets: Secret[];
}

// groupSecrets groups secrets by the folders they apply to, folders sorted,
// names sorted within each, keeping those that match filter (a name or a
// folder, any case). Secrets with no folder come last.
export function groupSecrets(account: string, secrets: Secret[], filter = ""): SecretGroup[] {
  const needle = filter.trim().toLowerCase();
  const byLabel = new Map<string, SecretGroup>();
  for (const secret of secrets) {
    const label = scopeLabel(account, secret.scope);
    if (needle && !(secret.name ?? "").toLowerCase().includes(needle) && !label.toLowerCase().includes(needle)) {
      continue;
    }
    const unused = secretPaths(account, secret.scope).length === 0 && (secret.scope?.slices ?? []).length === 0;
    const group = byLabel.get(label) ?? { label, unused, secrets: [] };
    group.secrets.push(secret);
    byLabel.set(label, group);
  }
  const out = [...byLabel.values()];
  for (const group of out) {
    group.secrets.sort((a, b) => (a.name ?? "").localeCompare(b.name ?? ""));
  }
  return out.sort((a, b) => Number(a.unused) - Number(b.unused) || a.label.localeCompare(b.label));
}
