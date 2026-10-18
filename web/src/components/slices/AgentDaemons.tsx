import { useAuth } from "@clerk/tanstack-react-start";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";

import type { AgentDaemon } from "../../api/types";
import { useApi } from "../../api/useApi";
import { daemonName } from "../../lib/daemons";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
import { formatRelativeTime } from "../../lib/time";
import { getErrorMessage } from "./SlicePageParts";

const MAX_NAME_LENGTH = 64;

// AgentDaemons is the "Agent daemons" section of your own account page: the
// daemons you started with gs agent start and those of agents you co-own, with
// their status, last seen time, and the slices that use each one for CI. Each
// can be renamed, and the new name shows in slice settings and on check runs.
// Errors resolve to no section rather than a misleading empty one.
export function AgentDaemons() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const enabled = Boolean(isLoaded && isSignedIn);
  const query = useQuery({
    enabled,
    queryKey: ["agentDaemons"],
    queryFn: async () => (await api.listDaemons({})).daemons ?? [],
    retry: false
  });
  if (!enabled || query.isPending || query.isError) {
    return null;
  }
  const daemons = query.data ?? [];

  return (
    <section aria-labelledby="agent-daemons-heading">
      <h2
        className="text-sm font-semibold text-zinc-950 dark:text-zinc-50"
        id="agent-daemons-heading"
      >
        Agent daemons
      </h2>
      <p className="text-sm leading-6 text-slate-600 dark:text-zinc-400">
        Machines running <code className="font-mono text-xs">gs agent start</code>, yours and
        those of agents you co-own. They run agent conversations and CI checks.
      </p>
      <div className="mt-3">
        {daemons.length === 0 ? (
          <div className="rounded-md border border-dashed border-slate-300 p-4 text-sm leading-6 text-slate-600 dark:border-zinc-700 dark:text-zinc-400">
            No daemons yet. On a machine with the agent runtime, run{" "}
            <code className="font-mono text-xs">gs agent start --name &lt;name&gt;</code> and it
            appears here.
          </div>
        ) : (
          <ul className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {daemons.map((daemon) => (
              <DaemonCard daemon={daemon} key={daemon.id ?? daemon.name} />
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

function DaemonCard({ daemon }: { daemon: AgentDaemon }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState("");
  const shown = daemonName(daemon);
  const renamed = Boolean(daemon.displayName) && daemon.displayName !== daemon.name;
  const online = daemon.status === "online";
  const ciSlices = daemon.ciSlices ?? [];

  const rename = useMutation({
    mutationFn: (displayName: string) =>
      api.renameDaemon({ daemonId: daemon.id, displayName }),
    onSuccess: async () => {
      setEditing(false);
      // The name shows in the pickers, slice settings and check runs too.
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["agentDaemons"] }),
        queryClient.invalidateQueries({ queryKey: ["sliceRef"] }),
        queryClient.invalidateQueries({ queryKey: ["checkRuns"] })
      ]);
    }
  });

  function startEditing() {
    rename.reset();
    setName(daemon.displayName || daemon.name || "");
    setEditing(true);
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const next = name.trim();
    if (!next || next === shown) {
      setEditing(false);
      return;
    }
    rename.mutate(next);
  }

  return (
    <li className="rounded-md border border-slate-200 bg-white px-3 py-2 shadow-sm dark:border-zinc-800 dark:bg-zinc-900">
      <div className="flex items-center justify-between gap-2">
        <p className="min-w-0 truncate text-sm font-medium text-zinc-950 dark:text-zinc-50" title={shown}>
          {shown}
        </p>
        <span
          className={
            online
              ? "inline-flex shrink-0 items-center gap-1.5 rounded-full bg-emerald-50 px-2 py-0.5 text-[11px] font-medium text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-300"
              : "inline-flex shrink-0 items-center gap-1.5 rounded-full bg-slate-100 px-2 py-0.5 text-[11px] font-medium text-slate-600 dark:bg-zinc-800 dark:text-zinc-400"
          }
        >
          <span
            aria-hidden="true"
            className={online ? "h-1.5 w-1.5 rounded-full bg-emerald-500" : "h-1.5 w-1.5 rounded-full bg-slate-400"}
          />
          {online ? "online" : "offline"}
        </span>
      </div>
      <p className="mt-1 truncate text-xs text-slate-500 dark:text-zinc-400">
        {[daemon.runtime, daemon.version].filter(Boolean).join(" ") || "runtime unknown"}
        {daemon.account ? ` · ${daemon.account}` : ""}
        {renamed ? ` · starts as ${daemon.name}` : ""}
      </p>
      <p className="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
        {online ? "connected" : daemon.lastSeenAt ? `last seen ${formatRelativeTime(daemon.lastSeenAt)}` : "never seen"}
        {daemon.mine === false ? " · an agent you co-own" : ""}
      </p>
      <p className="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
        {ciSlices.length === 0 ? (
          "Not a CI daemon of any slice"
        ) : (
          <>
            CI daemon of{" "}
            {ciSlices.map((ref, index) => {
              const params = toSliceRouteParams(ref);
              const label = `${ref.account}/${ref.slice}`;
              return (
                <span key={label}>
                  {index > 0 ? ", " : ""}
                  {params ? (
                    <Link
                      className="underline decoration-slate-300 underline-offset-2 hover:decoration-slate-700"
                      params={params}
                      to="/slices/$account/$slice/settings"
                    >
                      {label}
                    </Link>
                  ) : (
                    label
                  )}
                </span>
              );
            })}
          </>
        )}
      </p>

      {editing ? (
        <form className="mt-2 grid gap-2" onSubmit={submit}>
          <label className="grid gap-1 text-xs font-medium text-zinc-800 dark:text-zinc-200">
            Name
            <input
              autoFocus
              className="min-w-0 rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:focus:ring-zinc-700"
              maxLength={MAX_NAME_LENGTH}
              onChange={(event) => setName(event.target.value)}
              value={name}
            />
          </label>
          {rename.error ? (
            <p className="text-xs text-rose-700 dark:text-rose-300" role="alert">
              {getErrorMessage(rename.error)}
            </p>
          ) : null}
          <div className="flex flex-wrap items-center gap-2">
            <button
              className="rounded-md bg-zinc-950 px-2.5 py-1 text-xs font-semibold text-white transition hover:bg-zinc-800 disabled:opacity-60 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
              disabled={rename.isPending || !name.trim()}
              type="submit"
            >
              {rename.isPending ? "Saving…" : "Save"}
            </button>
            <button
              className="rounded-md border border-slate-300 bg-white px-2.5 py-1 text-xs font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
              onClick={() => setEditing(false)}
              type="button"
            >
              Cancel
            </button>
            {renamed ? (
              <button
                className="text-xs text-slate-600 underline underline-offset-2 hover:text-zinc-950 disabled:opacity-60 dark:text-zinc-400 dark:hover:text-zinc-50"
                disabled={rename.isPending}
                onClick={() => rename.mutate("")}
                type="button"
              >
                Use {daemon.name}
              </button>
            ) : null}
          </div>
        </form>
      ) : (
        <button
          aria-label={`Rename ${shown}`}
          className="mt-2 rounded-md border border-slate-300 bg-white px-2.5 py-1 text-xs font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
          onClick={startEditing}
          type="button"
        >
          Rename
        </button>
      )}
    </li>
  );
}
