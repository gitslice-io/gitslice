import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import type { TriggerDefinition, TriggerRun } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { Timestamp } from "../../components/Timestamp";
import { cn } from "../../lib/cn";
import { shortChangesetId } from "../../lib/objectId";
import { isFinalTriggerStatus, TriggerRunLog } from "../changeset-detail/TriggerRunsPanel";

// The triggers that apply at a folder (design/34_triggers.md): those defined
// in its .gitslice/triggers.yaml and in the folders above it, with their
// recent runs, so you can see what a change there sets off.

interface TriggersDrawerProps {
  api: ApiClient;
  // The canonical folder shown, such as /acme/services/billing.
  folder: string;
  onClose(): void;
  open: boolean;
}

export function TriggersDrawer({ api, folder, onClose, open }: TriggersDrawerProps) {
  const triggersQuery = useQuery({
    enabled: open && Boolean(folder),
    queryKey: ["triggers", folder],
    queryFn: () => api.listTriggers({ path: folder })
  });
  const triggers = triggersQuery.data?.triggers ?? [];
  const problems = triggersQuery.data?.problems ?? [];
  const account = folder.split("/").filter(Boolean)[0] ?? "";

  return (
    <>
      {open ? (
        <button aria-label="Close triggers" className="fixed inset-0 z-40 bg-black/30" onClick={onClose} type="button" />
      ) : null}
      <aside
        aria-hidden={!open}
        aria-label="Triggers"
        className={cn(
          "fixed inset-y-0 right-0 z-50 flex w-full max-w-lg transform flex-col bg-white dark:bg-zinc-900 shadow-xl transition-transform duration-200",
          open ? "translate-x-0" : "translate-x-full"
        )}
      >
        <div className="border-b border-slate-200 dark:border-zinc-800 px-4 py-4">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Triggers</h2>
              <p className="mt-1 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">{folder}</p>
              <p className="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                Defined here and in the folders above, at the head of main.{" "}
                <Link className="font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300" params={{ section: "triggers" } as never} to="/doc/$section">
                  How triggers work
                </Link>
              </p>
            </div>
            <button
              className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950"
              onClick={onClose}
              type="button"
            >
              Close
            </button>
          </div>
        </div>
        <div className="flex-1 overflow-y-auto px-4 py-4">
          {triggersQuery.isPending ? (
            <div className="grid gap-1.5">
              <div className="h-16 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
              <div className="h-16 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
            </div>
          ) : triggersQuery.isError ? (
            <SliceNotice title="Could not load triggers" tone="error">
              {triggersQuery.error?.message}
            </SliceNotice>
          ) : (
            <div className="grid gap-3">
              {problems.map((p) => (
                <SliceNotice key={p.file} title={`${p.file} does not parse`} tone="error">
                  {p.message}
                </SliceNotice>
              ))}
              {triggers.length === 0 && problems.length === 0 ? (
                <SliceNotice title="No triggers apply here.">
                  Add a .gitslice/triggers.yaml to this folder or one above it.
                </SliceNotice>
              ) : null}
              {triggers.map((trigger) => (
                <TriggerCard account={account} api={api} key={trigger.id} trigger={trigger} />
              ))}
            </div>
          )}
        </div>
      </aside>
    </>
  );
}

function TriggerCard({ account, api, trigger }: { account: string; api: ApiClient; trigger: TriggerDefinition }) {
  const queryClient = useQueryClient();
  const [openRun, setOpenRun] = useState("");
  const runsKey = ["triggerRunsOf", account, trigger.id];
  const runsQuery = useQuery({
    enabled: Boolean(account && trigger.id),
    queryKey: runsKey,
    queryFn: async () => (await api.listTriggerRuns({ account, triggerId: trigger.id, limit: 5 })).runs ?? [],
    refetchInterval: (query) => (query.state.data?.some((run) => !isFinalTriggerStatus(run.status)) ? 3500 : false)
  });
  const run = useMutation({
    mutationFn: () => api.runTrigger({ account, triggerId: trigger.id }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: runsKey })
  });
  const what =
    trigger.action === "http" ? trigger.httpUrl : trigger.action === "check" ? (trigger.checks ?? []).join(", ") : trigger.worker;
  const manual = (trigger.on ?? []).includes("manual");
  const runs = runsQuery.data ?? [];

  return (
    <section className="rounded-md border border-slate-200 dark:border-zinc-800">
      <div className="flex items-start justify-between gap-2 px-3 py-2.5">
        <div className="min-w-0">
          <p className="break-all font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">{trigger.id}</p>
          <p className="mt-0.5 text-xs text-slate-600 dark:text-zinc-400">
            on {(trigger.on ?? []).join(", ")} · {trigger.action} <span className="break-all font-mono">{what}</span>
          </p>
          {(trigger.paths ?? []).length > 0 ? (
            <p className="mt-0.5 break-all text-xs text-slate-500 dark:text-zinc-400">paths {(trigger.paths ?? []).join(" ")}</p>
          ) : null}
          {trigger.action !== "check" && (trigger.checks ?? []).length > 0 ? (
            <p className="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">reports {(trigger.checks ?? []).join(", ")}</p>
          ) : null}
        </div>
        {manual ? (
          <button
            className="shrink-0 rounded-md border border-slate-300 dark:border-zinc-700 px-2 py-1 text-xs font-semibold text-slate-700 dark:text-zinc-300 hover:bg-slate-50 dark:hover:bg-zinc-950 disabled:opacity-60"
            disabled={run.isPending}
            onClick={() => run.mutate()}
            type="button"
          >
            {run.isPending ? "Starting…" : "Run"}
          </button>
        ) : null}
      </div>
      {run.error ? <p className="px-3 pb-2 text-xs text-rose-700 dark:text-rose-300">{run.error.message}</p> : null}
      <div className="border-t border-slate-100 dark:border-zinc-800">
        {runsQuery.isPending ? null : runs.length === 0 ? (
          <p className="px-3 py-2 text-xs text-slate-500 dark:text-zinc-400">No runs yet.</p>
        ) : (
          <ul className="divide-y divide-slate-100 dark:divide-zinc-800">
            {runs.map((r) => (
              <RunRow key={r.id} onToggle={() => setOpenRun(openRun === r.id ? "" : (r.id ?? ""))} open={openRun === r.id} run={r} />
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

function RunRow({ open, onToggle, run }: { open: boolean; onToggle(): void; run: TriggerRun }) {
  const handle = shortChangesetId(run.changesetId ?? "");
  const status = (run.status ?? "").toLowerCase();
  return (
    <li>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 py-2 text-xs">
        <button aria-expanded={open} className="font-semibold text-zinc-900 dark:text-zinc-100" onClick={onToggle} type="button">
          <span
            className={cn(
              "mr-1.5 inline-block h-1.5 w-1.5 rounded-full",
              status === "succeeded" ? "bg-emerald-500" : status === "failed" || status === "errored" || status === "timed_out" ? "bg-rose-500" : isFinalTriggerStatus(status) ? "bg-slate-400" : "animate-pulse bg-amber-500"
            )}
          />
          {status.replace("_", " ")}
        </button>
        <span className="text-slate-500 dark:text-zinc-400">{run.eventKind}</span>
        {handle ? (
          <Link className="font-mono text-sky-700 underline-offset-2 hover:underline dark:text-sky-300" params={{ id: handle }} to="/cs/$id">
            {handle}
          </Link>
        ) : null}
        <span className="text-slate-500 dark:text-zinc-400">
          <Timestamp relative value={run.createdAt} />
        </span>
        {run.error ? <span className="w-full text-rose-700 dark:text-rose-300">{run.error}</span> : null}
      </div>
      {open ? <TriggerRunLog run={run} /> : null}
    </li>
  );
}
