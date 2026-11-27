import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import type { AgentDaemon, Slice, SliceRef } from "../../api/types";
import { useApi } from "../../api/useApi";
import { daemonName } from "../../lib/daemons";
import { accountCIDaemonsQuery, accountSlicesKeyPrefix } from "../../queries/accounts";
import { sliceDaemonsQuery } from "../../queries/agents";
import { sliceRefKey, slicesKeyPrefix } from "../../queries/slices";
import { SlicePanel, getErrorMessage } from "./SlicePageParts";

// A change to the slice's pool of CI daemons: daemonId alone replaces it,
// add appends to it and remove takes one out.
type CIDaemonChange = { daemonId: string; add?: boolean; remove?: boolean };

const selectClass =
  "min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm text-zinc-950 dark:text-zinc-50 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700 disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-500 dark:disabled:text-zinc-400";

export function SliceCIDaemonPanel({
  canEdit,
  routeAccount,
  routeSlice,
  slice
}: {
  canEdit: boolean;
  routeAccount: string;
  routeSlice: string;
  slice: Slice;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [ciSaveMessage, setCiSaveMessage] = useState<string | null>(null);

  const daemonsQuery = useQuery({
    ...sliceDaemonsQuery(api, { account: routeAccount, slice: routeSlice }),
    enabled: Boolean(slice)
  });

  const accountPoolQuery = useQuery({
    ...accountCIDaemonsQuery(api, routeAccount),
    enabled: Boolean(routeAccount)
  });
  const accountPoolIds = accountPoolQuery.data?.daemonIds ?? [];
  const accountPoolNames = accountPoolQuery.data?.daemonNames ?? [];

  const ciDaemonMutation = useMutation({
    mutationFn: async (change: CIDaemonChange) => {
      const fallbackRef: SliceRef | undefined =
        routeAccount && routeSlice ? { account: routeAccount, slice: routeSlice } : undefined;
      const targetSlice = slice.ref ?? fallbackRef;
      if (!targetSlice?.account || !targetSlice.slice) {
        throw new Error("Slice reference is not available.");
      }

      return api.setSliceCIDaemon({
        ...change,
        slice: targetSlice
      });
    },
    onError: () => {
      setCiSaveMessage(null);
    },
    onMutate: () => {
      setCiSaveMessage(null);
    },
    onSuccess: async (updatedSlice) => {
      setCiSaveMessage("CI daemon updated.");
      queryClient.setQueryData(
        sliceRefKey(routeAccount, routeSlice),
        updatedSlice
      );
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: sliceRefKey(routeAccount, routeSlice)
        }),
        queryClient.invalidateQueries({ queryKey: slicesKeyPrefix }),
        queryClient.invalidateQueries({ queryKey: accountSlicesKeyPrefix })
      ]);
    }
  });

  const daemons = daemonsQuery.data ?? [];
  const currentDaemonId = slice.ciDaemonId ?? "";
  // The name the server shows the current daemon by, known even to people who
  // cannot list it.
  const currentDaemonName = slice.ciDaemonName ?? "";
  const onlineDaemons = daemons.filter((daemon) => daemon.status === "online");
  const currentDaemon = daemons.find((daemon) => daemon.id === currentDaemonId);
  const currentIsOnline = onlineDaemons.some(
    (daemon) => daemon.id === currentDaemonId
  );
  const canUpdate = canEdit && Boolean(slice.ref?.account && slice.ref.slice);
  const disabled = daemonsQuery.isPending || ciDaemonMutation.isPending || !canUpdate;
  // The pool (design/32): each run goes to the first one online.
  const poolIds = slice.ciDaemonIds?.length ? slice.ciDaemonIds : currentDaemonId ? [currentDaemonId] : [];
  const pool = poolIds.map((id, index) => ({
    id,
    name:
      slice.ciDaemonNames?.[index] ||
      (id === currentDaemonId ? currentDaemonName : "") ||
      daemonName(daemons.find((daemon) => daemon.id === id), id)
  }));
  const backups = onlineDaemons.filter((daemon) => daemon.id && !poolIds.includes(daemon.id));
  const error = daemonsQuery.error
    ? getErrorMessage(daemonsQuery.error)
    : ciDaemonMutation.error
      ? getErrorMessage(ciDaemonMutation.error)
      : "";

  return (
    <SlicePanel>
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(16rem,24rem)] lg:items-start">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">CI daemon</h2>
          <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            By default this slice inherits{" "}
            <Link
              className="font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
              params={{ account: routeAccount }}
              to="/accounts/$account"
            >
              @{routeAccount}
            </Link>
            &apos;s account CI runners. Set a daemon here only to override the account pool for this slice.
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
                    <code className="block break-all font-mono text-xs text-slate-500 dark:text-zinc-400">{member.id}</code>
                  </span>
                  <button
                    className="shrink-0 rounded-md border border-slate-300 bg-white px-2 py-1 text-xs font-medium text-slate-700 transition hover:bg-slate-50 disabled:opacity-60 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
                    disabled={disabled}
                    onClick={() => ciDaemonMutation.mutate({ daemonId: member.id, remove: true })}
                    type="button"
                  >
                    Remove
                  </button>
                </li>
              ))}
            </ol>
          ) : currentDaemonId ? (
            <>
              <p className="mt-1 break-words text-sm font-medium text-zinc-950 dark:text-zinc-50">
                {currentDaemonName || daemonName(currentDaemon, currentDaemonId)}
              </p>
              <code className="mt-0.5 block break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
                {currentDaemonId}
              </code>
            </>
          ) : accountPoolIds.length > 0 ? (
            <p className="mt-1 text-sm text-zinc-950 dark:text-zinc-50">
              Inherited from @{routeAccount}:{" "}
              <span className="font-medium">
                {accountPoolIds
                  .map((id, i) => accountPoolNames[i] || id)
                  .join(", ")}
              </span>
            </p>
          ) : (
            <p className="mt-1 text-sm text-zinc-950 dark:text-zinc-50">none</p>
          )}
        </div>

        <div className="grid gap-2">
          <label className="grid gap-1.5 text-sm font-medium text-zinc-800 dark:text-zinc-200">
            CI daemon
            <select
              aria-label="CI daemon"
              className={selectClass}
              disabled={disabled}
              onChange={(event) => ciDaemonMutation.mutate({ daemonId: event.target.value })}
              value={currentDaemonId}
            >
              <option value="">
                {accountPoolIds.length > 0 ? `Inherit from @${routeAccount}` : "None"}
              </option>
              {currentDaemonId && !currentIsOnline ? (
                <option value={currentDaemonId}>
                  {daemonLabel(currentDaemon, currentDaemonId)} (current)
                </option>
              ) : null}
              {onlineDaemons.map((daemon) => (
                <option key={daemon.id ?? daemon.name} value={daemon.id ?? ""}>
                  {daemonLabel(daemon, daemon.id ?? "")}
                </option>
              ))}
            </select>
          </label>
          {currentDaemonId && backups.length > 0 ? (
            <label className="grid gap-1.5 text-sm font-medium text-zinc-800 dark:text-zinc-200">
              Add a backup
              <select
                aria-label="Add a backup CI daemon"
                className={selectClass}
                disabled={disabled}
                onChange={(event) => {
                  if (event.target.value) {
                    ciDaemonMutation.mutate({ daemonId: event.target.value, add: true });
                  }
                }}
                value=""
              >
                <option value="">Choose a daemon</option>
                {backups.map((daemon) => (
                  <option key={daemon.id} value={daemon.id}>
                    {daemonLabel(daemon, daemon.id ?? "")}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
          {daemonsQuery.isPending ? (
            <p className="text-xs text-slate-500 dark:text-zinc-400">Loading daemons...</p>
          ) : onlineDaemons.length === 0 ? (
            <p className="text-xs leading-5 text-slate-500 dark:text-zinc-400">
              No online daemons are available. You can still clear the current
              runner by choosing None.
            </p>
          ) : null}
          {error ? (
            <p className="rounded-md border border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 px-3 py-2 text-sm text-rose-900 dark:text-rose-200">
              {error}
            </p>
          ) : ciSaveMessage ? (
            <p className="rounded-md border border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 px-3 py-2 text-sm text-emerald-900 dark:text-emerald-200">
              {ciSaveMessage}
            </p>
          ) : null}
        </div>
      </div>
    </SlicePanel>
  );
}

function daemonLabel(daemon: AgentDaemon | undefined, fallback: string) {
  if (!daemon) {
    return fallback;
  }
  const name = daemonName(daemon, fallback);
  const runtime = [daemon.runtime, daemon.version].filter(Boolean).join(" ");
  return runtime ? `${name} - ${runtime}` : name;
}
