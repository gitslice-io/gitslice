import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import type { AgentDaemon } from "../../api/types";
import { useApi } from "../../api/useApi";
import { daemonName } from "../../lib/daemons";
import { accountCIDaemonsKey, accountCIDaemonsQuery } from "../../queries/accounts";
import { agentDaemonsKey, agentDaemonsQuery } from "../../queries/agents";
import { SlicePanel, getErrorMessage } from "./SlicePageParts";

type AccountCIDaemonChange = { daemonId: string; add?: boolean; remove?: boolean };

const selectClass =
  "min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm text-zinc-950 dark:text-zinc-50 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700 disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-500 dark:disabled:text-zinc-400";

export function AccountCIDaemonPanel({
  account,
  canEdit,
  className = "mt-10"
}: {
  account: string;
  canEdit: boolean;
  className?: string;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [saveMessage, setSaveMessage] = useState<string | null>(null);

  const poolQuery = useQuery({
    ...accountCIDaemonsQuery(api, account),
    enabled: Boolean(account)
  });

  const allDaemonsQuery = useQuery({
    ...agentDaemonsQuery(api),
    enabled: Boolean(account && canEdit && typeof api.listDaemons === "function")
  });

  const mutation = useMutation({
    mutationFn: (change: AccountCIDaemonChange) =>
      api.setAccountCIDaemon({ account, ...change }),
    onError: () => setSaveMessage(null),
    onMutate: () => setSaveMessage(null),
    onSuccess: async (updated) => {
      setSaveMessage("CI runner pool updated.");
      queryClient.setQueryData(accountCIDaemonsKey(account), updated);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: accountCIDaemonsKey(account) }),
        queryClient.invalidateQueries({ queryKey: agentDaemonsKey })
      ]);
    }
  });

  const poolIds = poolQuery.data?.daemonIds ?? [];
  const poolNames = poolQuery.data?.daemonNames ?? [];
  const daemons = (allDaemonsQuery.data ?? []).filter((d) => d.manageable !== false);
  const onlineDaemons = daemons.filter((d) => d.status === "online");
  const primaryId = poolIds[0] ?? "";
  const primaryDaemon = daemons.find((d) => d.id === primaryId);
  const primaryIsOnline = onlineDaemons.some((d) => d.id === primaryId);

  const pool = poolIds.map((id, index) => ({
    id,
    name: poolNames[index] || daemonName(daemons.find((d) => d.id === id), id)
  }));
  const backups = onlineDaemons.filter((d) => d.id && !poolIds.includes(d.id));
  const disabled = poolQuery.isPending || allDaemonsQuery.isPending || mutation.isPending || !canEdit;

  const error = poolQuery.error
    ? getErrorMessage(poolQuery.error)
    : allDaemonsQuery.error
      ? getErrorMessage(allDaemonsQuery.error)
      : mutation.error
        ? getErrorMessage(mutation.error)
        : "";

  if (!canEdit && pool.length === 0) {
    return null;
  }

  return (
    <section aria-label="Default CI runner pool" className={className || undefined}>
      <SlicePanel>
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(16rem,24rem)] lg:items-start">
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
              Default CI runner pool
            </h2>
            <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
              Default runner pool for <span className="font-mono">/{account}</span>. Folder automations
              and <span className="font-mono">.gitslice/checks.yaml</span> across every slice run on
              the first online runner here unless a slice sets its own override.
            </p>
            <p className="mt-3 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
              {pool.length > 1 ? "Pool" : "Current"}
            </p>
            {pool.length > 1 ? (
              <ol className="mt-1 grid gap-2">
                {pool.map((member, index) => (
                  <li className="flex items-start justify-between gap-3" key={member.id}>
                    <span className="min-w-0">
                      <span className="block break-words text-sm font-medium text-zinc-950 dark:text-zinc-50">
                        {index + 1}. {member.name}
                      </span>
                      <code className="block break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
                        {member.id}
                      </code>
                    </span>
                    {canEdit ? (
                      <button
                        className="shrink-0 rounded-md border border-slate-300 bg-white px-2 py-1 text-xs font-medium text-slate-700 transition hover:bg-slate-50 disabled:opacity-60 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
                        disabled={disabled}
                        onClick={() => mutation.mutate({ daemonId: member.id, remove: true })}
                        type="button"
                      >
                        Remove
                      </button>
                    ) : null}
                  </li>
                ))}
              </ol>
            ) : pool.length === 1 ? (
              <>
                <p className="mt-1 break-words text-sm font-medium text-zinc-950 dark:text-zinc-50">
                  {pool[0].name}
                </p>
                <code className="mt-0.5 block break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
                  {pool[0].id}
                </code>
              </>
            ) : (
              <p className="mt-1 text-sm text-slate-600 dark:text-zinc-400">
                No account CI runners configured
              </p>
            )}
          </div>

          {canEdit ? (
            <div className="grid gap-2">
              <label className="grid gap-1.5 text-sm font-medium text-zinc-800 dark:text-zinc-200">
                Account CI runner
                <select
                  aria-label="Account CI runner"
                  className={selectClass}
                  disabled={disabled}
                  onChange={(event) => mutation.mutate({ daemonId: event.target.value })}
                  value={primaryId}
                >
                  <option value="">None</option>
                  {primaryId && !primaryIsOnline ? (
                    <option value={primaryId}>
                      {daemonLabel(primaryDaemon, pool[0]?.name || primaryId)} (current)
                    </option>
                  ) : null}
                  {onlineDaemons.map((daemon) => (
                    <option key={daemon.id ?? daemon.name} value={daemon.id ?? ""}>
                      {daemonLabel(daemon, daemon.id ?? "")}
                    </option>
                  ))}
                </select>
              </label>

              {primaryId && backups.length > 0 ? (
                <label className="grid gap-1.5 text-sm font-medium text-zinc-800 dark:text-zinc-200">
                  Add backup runner
                  <select
                    aria-label="Add backup CI runner"
                    className={selectClass}
                    disabled={disabled}
                    onChange={(event) => {
                      if (event.target.value) {
                        mutation.mutate({ daemonId: event.target.value, add: true });
                      }
                    }}
                    value=""
                  >
                    <option value="">Choose a runner</option>
                    {backups.map((daemon) => (
                      <option key={daemon.id} value={daemon.id}>
                        {daemonLabel(daemon, daemon.id ?? "")}
                      </option>
                    ))}
                  </select>
                </label>
              ) : null}

              {allDaemonsQuery.isPending ? (
                <p className="text-xs text-slate-500 dark:text-zinc-400">Loading runners…</p>
              ) : onlineDaemons.length === 0 ? (
                <p className="text-xs leading-5 text-slate-500 dark:text-zinc-400">
                  No online CI runners found. Start one with{" "}
                  <code className="font-mono">gs agent start --ci --name &lt;name&gt;</code>.
                </p>
              ) : null}

              {error ? (
                <p className="rounded-md border border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 px-3 py-2 text-sm text-rose-900 dark:text-rose-200">
                  {error}
                </p>
              ) : saveMessage ? (
                <p className="rounded-md border border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 px-3 py-2 text-sm text-emerald-900 dark:text-emerald-200">
                  {saveMessage}
                </p>
              ) : null}
            </div>
          ) : null}
        </div>
      </SlicePanel>
    </section>
  );
}

function daemonLabel(daemon: AgentDaemon | undefined, fallback: string) {
  if (!daemon) {
    return fallback;
  }
  const name = daemonName(daemon, fallback);
  const runtime = [daemon.runtime?.toLowerCase() === "ci" ? "" : daemon.runtime, daemon.version]
    .filter(Boolean)
    .join(" ");
  return runtime ? `${name} - ${runtime}` : name;
}
