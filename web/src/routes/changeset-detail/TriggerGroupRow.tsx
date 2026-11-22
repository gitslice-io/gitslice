import type { AutomationRun, CheckRun } from "../../api/types";
import type { useApi } from "../../api/useApi";
import { AutomationRunLog, TriggerStatusBadge, isFinalTriggerStatus } from "../../components/automations/AutomationRunLog";
import { Timestamp } from "../../components/Timestamp";
import { Button } from "../../components/ui/Button";
import { cn } from "../../lib/cn";
import { CheckRunItem } from "./CheckRunItem";

export interface TriggerGroupRowProps {
  api: ReturnType<typeof useApi>;
  cancelPending: boolean;
  checks: CheckRun[];
  onCancelTrigger: (runId: string) => void;
  onRerunCheck: (runId: string) => void;
  onRerunTrigger: (runId: string) => void;
  onToggleCheck: (runId: string) => void;
  onToggleTriggerLog: (runId: string) => void;
  openCheckId: string;
  openTriggerLogId: string;
  rerunCheckPendingId: string;
  rerunTriggerPending: boolean;
  trigger: AutomationRun;
}

export function TriggerGroupRow({
  api,
  cancelPending,
  checks,
  onCancelTrigger,
  onRerunCheck,
  onRerunTrigger,
  onToggleCheck,
  onToggleTriggerLog,
  openCheckId,
  openTriggerLogId,
  rerunCheckPendingId,
  rerunTriggerPending,
  trigger
}: TriggerGroupRowProps) {
  const id = trigger.id ?? "";
  const logOpen = openTriggerLogId === id;
  const final = isFinalTriggerStatus(trigger.status);

  return (
    <li className={cn(logOpen && "bg-slate-50/70 dark:bg-zinc-950/60")}>
      <div className="grid grid-cols-1 gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto] md:px-5">
        <button
          aria-expanded={logOpen}
          className="min-w-0 text-left"
          onClick={() => onToggleTriggerLog(id)}
          type="button"
        >
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">
              {trigger.automationId}
            </span>
            <TriggerStatusBadge status={trigger.status} />
            <span className="text-xs text-slate-500 dark:text-zinc-400">
              {trigger.eventKind} · {trigger.action} · <Timestamp relative value={trigger.createdAt} />
            </span>
          </div>
          {trigger.error ? (
            <p className="mt-1 line-clamp-2 text-sm text-rose-700 dark:text-rose-300">
              {trigger.error}
            </p>
          ) : null}
        </button>

        <div className="flex items-center gap-2 md:justify-end">
          <button
            className="text-xs font-medium text-slate-500 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
            onClick={() => onToggleTriggerLog(id)}
            type="button"
          >
            {logOpen ? "Hide log" : checks.length > 0 ? "Trigger log" : "View log"}
          </button>
          {final && trigger.action ? (
            <Button
              disabled={rerunTriggerPending}
              onClick={() => onRerunTrigger(id)}
              size="xs"
            >
              Rerun
            </Button>
          ) : null}
          {!final ? (
            <Button
              disabled={cancelPending}
              onClick={() => onCancelTrigger(id)}
              size="xs"
            >
              Cancel
            </Button>
          ) : null}
        </div>
      </div>

      {logOpen ? <AutomationRunLog run={trigger} /> : null}

      {checks.length > 0 ? (
        <div className="px-3 pb-3 md:px-5">
          <div className="space-y-2 border-l-2 border-slate-200 dark:border-zinc-800 pl-3">
            {checks.map((check) => {
              const checkId = check.id ?? "";
              return (
                <CheckRunItem
                  api={api}
                  key={checkId || check.checkName || check.status}
                  nested
                  onRerun={onRerunCheck}
                  onToggle={onToggleCheck}
                  rerunning={rerunCheckPendingId === checkId}
                  run={check}
                  selected={openCheckId === checkId}
                />
              );
            })}
          </div>
        </div>
      ) : null}
    </li>
  );
}
