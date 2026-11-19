import type { CheckRun, TriggerRun } from "../../api/types";
import { sortCheckRuns } from "./checkStatus";

export interface TriggerGroup {
  checks: CheckRun[];
  trigger: TriggerRun;
}

export interface GroupedAutomation {
  primaryTriggers: TriggerGroup[];
  skippedTriggers: TriggerRun[];
  standaloneChecks: CheckRun[];
}

const finalTriggerStatuses = new Set(["succeeded", "failed", "errored", "canceled", "timed_out"]);

function triggerPriority(status?: string): number {
  const s = (status ?? "queued").toLowerCase();
  if (s === "failed" || s === "errored" || s === "timed_out") return 0;
  if (!finalTriggerStatuses.has(s)) return 1;
  if (s === "succeeded") return 2;
  return 3;
}

/**
 * Groups a patchset's trigger runs and check runs into a Triggers-first view:
 * - Checks whose triggerRunId matches a trigger run are nested under that trigger.
 * - `action: "check"` triggers that succeeded with 0 spawned checks (e.g. ancestor
 *   folder triggers whose path filters matched nothing) are separated into
 *   `skippedTriggers` so they can be collapsed cleanly.
 * - Any check without a matching trigger run stays in `standaloneChecks`.
 */
export function groupTriggersAndChecks(
  triggerRuns: TriggerRun[],
  checkRuns: CheckRun[],
  patchsetId: string
): GroupedAutomation {
  const relevantTriggers = triggerRuns.filter(
    (run) => !run.patchsetId || run.patchsetId === patchsetId
  );
  const triggerIds = new Set(
    relevantTriggers.map((run) => run.id ?? "").filter(Boolean)
  );

  const checksByTrigger = new Map<string, CheckRun[]>();
  const standaloneChecks: CheckRun[] = [];

  for (const check of checkRuns) {
    const parentId = check.triggerRunId ?? "";
    if (parentId && triggerIds.has(parentId)) {
      const bucket = checksByTrigger.get(parentId) ?? [];
      bucket.push(check);
      checksByTrigger.set(parentId, bucket);
    } else {
      standaloneChecks.push(check);
    }
  }

  const primaryTriggers: TriggerGroup[] = [];
  const skippedTriggers: TriggerRun[] = [];

  for (const trigger of relevantTriggers) {
    const id = trigger.id ?? "";
    const checks = sortCheckRuns(checksByTrigger.get(id) ?? []);
    const status = (trigger.status ?? "").toLowerCase();
    const isNoopCheckTrigger =
      trigger.action === "check" && status === "succeeded" && checks.length === 0;

    if (isNoopCheckTrigger) {
      skippedTriggers.push(trigger);
    } else {
      primaryTriggers.push({ trigger, checks });
    }
  }

  primaryTriggers.sort((a, b) => triggerPriority(a.trigger.status) - triggerPriority(b.trigger.status));

  return {
    primaryTriggers,
    skippedTriggers,
    standaloneChecks: sortCheckRuns(standaloneChecks)
  };
}
