import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import type {
  AutomationDefinition,
  AutomationRun,
  FolderCheckDefinition,
  FolderPolicyProblem
} from "../../../api/types";
import type { ApiClient } from "../../../api/useApi";
import { AutomationRunLog, isFinalTriggerStatus } from "../../../components/automations/AutomationRunLog";
import { SliceNotice } from "../../../components/slices/SlicePageParts";
import { Timestamp } from "../../../components/Timestamp";
import { Button } from "../../../components/ui/Button";
import { cn } from "../../../lib/cn";
import { shortChangesetId } from "../../../lib/objectId";
import { triggerRunsOfKey, triggerRunsOfQuery } from "../../../queries/automations";
import { needsRunModal, RunAutomationModal } from "../RunAutomationModal";
import { CheckCard, InheritanceBadge, ProblemNotices } from "./PolicyShared";

export interface PolicyAutomationsTabProps {
  account: string;
  api: ApiClient;
  checks: FolderCheckDefinition[];
  folder: string;
  problems: FolderPolicyProblem[];
  triggers: AutomationDefinition[];
}

export function PolicyAutomationsTab({
  account,
  api,
  checks,
  folder,
  problems,
  triggers
}: PolicyAutomationsTabProps) {
  return (
    <div className="grid min-w-0 gap-5 px-4 py-4">
      <ProblemNotices problems={problems} />

      <section aria-label="Checks" className="min-w-0">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">
            Checks
          </h3>
          <Link
            className="shrink-0 text-xs font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
            params={{ section: "checks" } as never}
            to="/doc/$section"
          >
            checks.yaml docs
          </Link>
        </div>
        {checks.length === 0 ? (
          <SliceNotice title="No checks apply here.">
            Add a <code>.gitslice/checks.yaml</code> to this folder or one above it. Mark a check{" "}
            <code>required: true</code> to gate submissions touching its paths.
          </SliceNotice>
        ) : (
          <div className="grid min-w-0 gap-2">
            {checks.map((check) => (
              <CheckCard check={check} folder={folder} key={check.id} />
            ))}
          </div>
        )}
      </section>

      <section aria-label="Automations" className="min-w-0">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">
            Automations
          </h3>
          <Link
            className="text-xs font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
            params={{ section: "automations" } as never}
            to="/doc/$section"
          >
            automations.yaml docs
          </Link>
        </div>
        {triggers.length === 0 ? (
          <SliceNotice title="No automations apply here.">
            Add a <code>.gitslice/automations.yaml</code> to this folder or one above it.
          </SliceNotice>
        ) : (
          <div className="grid gap-3">
            {triggers.map((trigger) => (
              <TriggerCard account={account} api={api} folder={folder} key={trigger.id} trigger={trigger} />
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

function TriggerCard({
  account,
  api,
  folder,
  trigger
}: {
  account: string;
  api: ApiClient;
  folder: string;
  trigger: AutomationDefinition;
}) {
  const queryClient = useQueryClient();
  const [openRun, setOpenRun] = useState("");
  const [modalOpen, setModalOpen] = useState(false);
  const runsKey = triggerRunsOfKey(account, trigger.id);
  const runsQuery = useQuery({
    ...triggerRunsOfQuery(api, account, trigger.id),
    enabled: Boolean(account && trigger.id),
    refetchInterval: (query) => (query.state.data?.some((run) => !isFinalTriggerStatus(run.status)) ? 3500 : false)
  });
  const run = useMutation({
    mutationFn: (inputs?: Record<string, string>) =>
      api.runAutomation(inputs && Object.keys(inputs).length > 0 ? { account, automationId: trigger.id, inputs } : { account, automationId: trigger.id }),
    onSuccess: (created) => {
      setModalOpen(false);
      if (created?.id) {
        setOpenRun(created.id);
      }
      return queryClient.invalidateQueries({ queryKey: runsKey });
    }
  });
  const stepCount = (trigger.steps ?? []).length;
  const what =
    trigger.action === "http"
      ? trigger.httpUrl
      : trigger.action === "check"
        ? (trigger.checks ?? []).join(", ")
        : trigger.action === "uses"
          ? trigger.uses?.ref ?? ""
          : trigger.action === "steps"
            ? `${stepCount} step${stepCount === 1 ? "" : "s"}`
            : trigger.action === "bash"
              ? [trigger.bash?.runtime || "base", trigger.egress || "offline", trigger.bash?.script].filter(Boolean).join(" · ")
              : trigger.worker;
  const manual = (trigger.on ?? []).includes("manual");
  const reusable = (trigger.on ?? []).includes("call");
  const runs = runsQuery.data ?? [];

  function handleRunClick() {
    if (needsRunModal(trigger)) {
      run.reset();
      setModalOpen(true);
      return;
    }
    run.mutate(undefined);
  }

  return (
    <section className="min-w-0 rounded-md border border-slate-200 dark:border-zinc-800">
      <div className="flex items-start justify-between gap-2 px-3 py-2.5">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            <p className="break-all font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">{trigger.id}</p>
            {reusable ? (
              <span className="rounded border border-indigo-200 bg-indigo-50 px-1.5 py-px text-[11px] font-semibold text-indigo-800 dark:border-indigo-900/60 dark:bg-indigo-950/40 dark:text-indigo-200">
                Reusable
              </span>
            ) : null}
            <InheritanceBadge currentFolder={folder} sourceFolder={trigger.folder ?? ""} />
          </div>
          {trigger.description ? (
            <p className="mt-0.5 text-xs text-slate-600 dark:text-zinc-300">{trigger.description}</p>
          ) : null}
          <p className="mt-0.5 text-xs text-slate-600 dark:text-zinc-400">
            on {(trigger.on ?? []).join(", ")} · {trigger.action}{" "}
            <span className="break-all font-mono">{what}</span>
          </p>
          {(trigger.paths ?? []).length > 0 ? (
            <p className="mt-0.5 break-all text-xs text-slate-500 dark:text-zinc-400">
              paths {(trigger.paths ?? []).join(" ")}
            </p>
          ) : null}
          {trigger.action !== "check" && (trigger.checks ?? []).length > 0 ? (
            <p className="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
              reports {(trigger.checks ?? []).join(", ")}
            </p>
          ) : null}
        </div>
        {manual ? (
          <Button
            className="shrink-0 font-semibold"
            disabled={run.isPending}
            onClick={handleRunClick}
            size="xs"
            variant="secondary"
          >
            {run.isPending ? "Starting…" : "Run"}
          </Button>
        ) : null}
      </div>
      {!modalOpen && run.error ? <p className="px-3 pb-2 text-xs text-rose-700 dark:text-rose-300">{run.error.message}</p> : null}
      <div className="border-t border-slate-100 dark:border-zinc-800">
        {runsQuery.isPending ? null : runs.length === 0 ? (
          <p className="px-3 py-2 text-xs text-slate-500 dark:text-zinc-400">No runs yet.</p>
        ) : (
          <ul className="divide-y divide-slate-100 dark:divide-zinc-800">
            {runs.map((r) => (
              <TriggerRunRow
                key={r.id}
                onToggle={() => setOpenRun(openRun === r.id ? "" : (r.id ?? ""))}
                open={openRun === r.id}
                run={r}
              />
            ))}
          </ul>
        )}
      </div>
      <RunAutomationModal
        automation={trigger}
        busy={run.isPending}
        error={run.error?.message}
        onClose={() => setModalOpen(false)}
        onSubmit={(inputs) => run.mutate(inputs)}
        open={modalOpen}
      />
    </section>
  );
}

function TriggerRunRow({ open, onToggle, run }: { open: boolean; onToggle(): void; run: AutomationRun }) {
  const handle = shortChangesetId(run.changesetId ?? "");
  const status = (run.status ?? "").toLowerCase();
  return (
    <li>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 py-2 text-xs">
        <button aria-expanded={open} className="font-semibold text-zinc-900 dark:text-zinc-100" onClick={onToggle} type="button">
          <span
            className={cn(
              "mr-1.5 inline-block h-1.5 w-1.5 rounded-full",
              status === "succeeded"
                ? "bg-emerald-500"
                : status === "failed" || status === "errored" || status === "timed_out"
                  ? "bg-rose-500"
                  : isFinalTriggerStatus(status)
                    ? "bg-slate-400"
                    : "animate-pulse bg-amber-500"
            )}
          />
          {status.replace("_", " ")}
        </button>
        <span className="text-slate-500 dark:text-zinc-400">{run.eventKind}</span>
        {run.templateId ? (
          <span className="rounded border border-indigo-200 bg-indigo-50 px-1.5 py-px font-mono text-[11px] text-indigo-800 dark:border-indigo-900/60 dark:bg-indigo-950/40 dark:text-indigo-200">
            uses {run.templateId}
          </span>
        ) : null}
        {run.triggeredBy ? (
          <span className="text-slate-500 dark:text-zinc-400">
            by <span className="font-mono">{run.triggeredBy}</span>
          </span>
        ) : null}
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
      {open ? <AutomationRunLog run={run} /> : null}
    </li>
  );
}
