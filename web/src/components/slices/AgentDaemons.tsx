import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";

import type { AgentDaemon, SliceRef } from "../../api/types";
import { useApi } from "../../api/useApi";
import { useAppAuth } from "../../auth/AppAuthProvider";
import { agentDaemonsKey, agentDaemonsQuery } from "../../queries/agents";
import { checkRunsAllKeyPrefix } from "../../queries/checks";
import { sliceRefKeyPrefix } from "../../queries/slices";
import { daemonName } from "../../lib/daemons";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
import { formatRelativeTime } from "../../lib/time";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { getErrorMessage } from "./SlicePageParts";

const MAX_NAME_LENGTH = 64;

// parseSliceInput reads "account/slice" or "account:slice".
function parseSliceInput(input: string): SliceRef | null {
  const match = /^([^/:\s]+)[/:]([^/:\s]+)$/.exec(input.trim());
  return match ? { account: match[1], slice: match[2] } : null;
}

// AgentDaemons lists the registered CI runners you started with
// `gs agent start --ci --name <name>` (and those of agents you co-own), with
// status, assignments, and an on-demand Manage panel for renaming and sharing
// a runner with specific slices. Errors resolve to no section rather than a
// misleading empty one.
export function AgentDaemons() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAppAuth();
  const enabled = Boolean(isLoaded && isSignedIn);
  const query = useQuery({
    ...agentDaemonsQuery(api),
    enabled,
    retry: false
  });
  if (!enabled || query.isPending || query.isError) {
    return null;
  }
  const daemons = query.data ?? [];

  return (
    <section aria-labelledby="ci-runners-heading">
      <h2
        className="text-sm font-semibold text-zinc-950 dark:text-zinc-50"
        id="ci-runners-heading"
      >
        Registered CI runners
      </h2>
      <p className="text-sm leading-6 text-slate-600 dark:text-zinc-400">
        Machines running <code className="font-mono text-xs">gs agent start --ci --name &lt;name&gt;</code>, yours
        and those of agents you co-own.
      </p>
      <div className="mt-3">
        {daemons.length === 0 ? (
          <div className="rounded-lg border border-dashed border-slate-300 p-4 text-sm leading-6 text-slate-600 dark:border-zinc-700 dark:text-zinc-400">
            No CI runners registered yet. On a machine that should run checks and folder automations, run{" "}
            <code className="font-mono text-xs">gs agent start --ci --name &lt;name&gt;</code> and it appears here.
          </div>
        ) : (
          <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/50">
            {daemons.map((daemon) => (
              <RunnerRow daemon={daemon} key={daemon.id ?? daemon.name} />
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

function RunnerRow({ daemon }: { daemon: AgentDaemon }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [managing, setManaging] = useState(false);
  const [name, setName] = useState("");
  const shown = daemonName(daemon);
  const renamed = Boolean(daemon.displayName) && daemon.displayName !== daemon.name;
  const online = daemon.status === "online";
  const ciSlices = daemon.ciSlices ?? [];
  const ciAccounts = daemon.ciAccounts ?? [];
  const exposedTo = daemon.exposedTo ?? [];
  const manageable = daemon.manageable !== false;
  const [exposeInput, setExposeInput] = useState("");
  const exposeRef = parseSliceInput(exposeInput);

  const refreshDaemons = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: agentDaemonsKey }),
      queryClient.invalidateQueries({ queryKey: sliceRefKeyPrefix })
    ]);
  };
  const expose = useMutation({
    mutationFn: (slice: SliceRef) => api.exposeDaemon({ daemonId: daemon.id, slice }),
    onSuccess: async () => {
      setExposeInput("");
      await refreshDaemons();
    }
  });
  const unexpose = useMutation({
    mutationFn: (slice: SliceRef) => api.unexposeDaemon({ daemonId: daemon.id, slice }),
    onSuccess: refreshDaemons
  });

  const rename = useMutation({
    mutationFn: (displayName: string) =>
      api.renameDaemon({ daemonId: daemon.id, displayName }),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: agentDaemonsKey }),
        queryClient.invalidateQueries({ queryKey: sliceRefKeyPrefix }),
        queryClient.invalidateQueries({ queryKey: checkRunsAllKeyPrefix })
      ]);
    }
  });

  function toggleManage() {
    if (!managing) {
      rename.reset();
      expose.reset();
      unexpose.reset();
      setName(daemon.displayName || daemon.name || "");
    }
    setManaging((open) => !open);
  }

  function submitRename(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const next = name.trim();
    if (!next || next === shown) {
      return;
    }
    rename.mutate(next);
  }

  const metaParts: string[] = [];
  if (daemon.version) {
    metaParts.push(`v${daemon.version.replace(/^v/, "")}`);
  }
  if (daemon.account) {
    metaParts.push(`@${daemon.account}`);
  }
  if (renamed) {
    metaParts.push(`starts as ${daemon.name}`);
  }
  metaParts.push(
    online
      ? "connected"
      : daemon.lastSeenAt
        ? `last seen ${formatRelativeTime(daemon.lastSeenAt)}`
        : "never seen"
  );
  if (daemon.mine === false) {
    metaParts.push("co-owned");
  }

  return (
    <li className="px-4 py-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <p className="truncate text-sm font-medium text-zinc-950 dark:text-zinc-50" title={shown}>
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
        <Button
          aria-expanded={managing}
          aria-label={`Manage ${shown}`}
          onClick={toggleManage}
          size="xs"
          variant="secondary"
        >
          {managing ? "Done" : "Manage"}
        </Button>
      </div>

      <p className="mt-1 text-xs text-slate-500 dark:text-zinc-400">
        {metaParts.join(" · ")}
      </p>

      <p className="mt-1 text-xs text-slate-500 dark:text-zinc-400">
        {ciAccounts.length === 0 && ciSlices.length === 0 ? (
          "Not assigned to an account or slice"
        ) : (
          <>
            {ciAccounts.length > 0 ? (
              <>
                Default runner for{" "}
                {ciAccounts.map((acct, index) => (
                  <span key={acct}>
                    {index > 0 ? ", " : ""}
                    <Link
                      className="underline decoration-slate-300 underline-offset-2 hover:decoration-slate-700"
                      params={{ account: acct }}
                      to="/accounts/$account"
                    >
                      @{acct}
                    </Link>
                  </span>
                ))}
                {ciSlices.length > 0 ? " · " : ""}
              </>
            ) : null}
            {ciSlices.length > 0 ? (
              <>
                Assigned to{" "}
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
            ) : null}
          </>
        )}
        {manageable && !managing && exposedTo.length > 0 ? (
          <>
            {" · "}Shared with {exposedTo.length} {exposedTo.length === 1 ? "slice" : "slices"}
          </>
        ) : null}
      </p>

      {managing ? (
        <div className="mt-3 grid gap-4 border-t border-slate-200 pt-3 dark:border-zinc-800 sm:grid-cols-2">
          <form className="grid content-start gap-2" onSubmit={submitRename}>
            <label className="grid gap-1 text-xs font-medium text-zinc-800 dark:text-zinc-200">
              Display name
              <Input
                className="h-8 px-2"
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
              <Button
                disabled={rename.isPending || !name.trim() || name.trim() === shown}
                size="xs"
                type="submit"
                variant="primary"
              >
                {rename.isPending ? "Saving…" : "Save name"}
              </Button>
              {renamed ? (
                <Button
                  className="underline underline-offset-2"
                  disabled={rename.isPending}
                  onClick={() => rename.mutate("")}
                  size="xs"
                  variant="ghost"
                >
                  Use {daemon.name}
                </Button>
              ) : null}
            </div>
          </form>

          {manageable ? (
            <div className="grid content-start gap-2 text-xs text-slate-600 dark:text-zinc-400">
              <p className="font-medium text-zinc-800 dark:text-zinc-200">Share with slices</p>
              <p>
                {exposedTo.length === 0
                  ? "Not shared with any slice yet. Share it so a slice owner can choose it as a CI runner."
                  : "Shared with (their owners can choose this runner for CI):"}
              </p>
              {exposedTo.length > 0 ? (
                <ul className="flex flex-wrap gap-1.5">
                  {exposedTo.map((ref) => {
                    const label = `${ref.account}/${ref.slice}`;
                    return (
                      <li
                        className="inline-flex items-center gap-1 rounded-full bg-slate-100 px-2 py-0.5 text-zinc-800 dark:bg-zinc-800 dark:text-zinc-200"
                        key={label}
                      >
                        {label}
                        <button
                          aria-label={`Stop sharing ${shown} with ${label}`}
                          className="text-slate-500 hover:text-zinc-950 disabled:opacity-60 dark:text-zinc-400 dark:hover:text-zinc-50"
                          disabled={unexpose.isPending}
                          onClick={() => unexpose.mutate(ref)}
                          type="button"
                        >
                          ×
                        </button>
                      </li>
                    );
                  })}
                </ul>
              ) : null}
              <form
                className="flex items-center gap-1.5"
                onSubmit={(event) => {
                  event.preventDefault();
                  if (exposeRef) {
                    expose.mutate(exposeRef);
                  }
                }}
              >
                <Input
                  aria-label={`Share ${shown} with a slice`}
                  className="flex-1"
                  onChange={(event) => setExposeInput(event.target.value)}
                  placeholder="account/slice"
                  size="sm"
                  value={exposeInput}
                />
                <Button
                  disabled={!exposeRef || expose.isPending}
                  size="xs"
                  type="submit"
                >
                  Share
                </Button>
              </form>
              {expose.error || unexpose.error ? (
                <p className="text-rose-700 dark:text-rose-300" role="alert">
                  {getErrorMessage(expose.error ?? unexpose.error)}
                </p>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}
