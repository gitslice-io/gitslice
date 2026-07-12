import { useMemo, type KeyboardEvent } from "react";

import type { Patchset } from "../../api/types";
import { cn } from "../../lib/cn";
import {
  TimelineStep,
  clamp,
  patchsetDotLabel,
  patchsetKey,
  patchsetOptionLabel,
  timelineIndexForValue
} from "./patchsetUtils";

export function PatchsetTimeline({
  currentPatchsetId,
  fromPatchset,
  onFromPatchsetChange,
  onToPatchsetChange,
  patchsets,
  toPatchset
}: {
  currentPatchsetId?: string;
  fromPatchset: string;
  onFromPatchsetChange(value: string): void;
  onToPatchsetChange(value: string): void;
  patchsets: Patchset[];
  toPatchset: string;
}) {
  const selectablePatchsets = patchsets.filter((patchset) => patchset.id);
  const steps = useMemo<TimelineStep[]>(
    () => [
      { id: "", label: "Base", patchset: undefined },
      ...selectablePatchsets.map((patchset) => ({
        id: patchset.id || "",
        label: patchsetDotLabel(patchset),
        patchset
      }))
    ],
    [selectablePatchsets]
  );
  const fromIndex = timelineIndexForValue(steps, fromPatchset, 0);
  const toIndex = Math.max(1, timelineIndexForValue(steps, toPatchset, steps.length - 1));
  const maxIndex = Math.max(0, steps.length - 1);
  // Keep adjacent snapshots a fixed distance apart instead of stretching the
  // track to 100% width — most changesets only have two snapshots, and an
  // edge-to-edge selector for two dots looks broken. The track only grows to
  // fill the container once there are enough patchsets to need the room.
  const STEP_GAP_PX = 104;
  const trackWidth = maxIndex > 0 ? `${maxIndex * STEP_GAP_PX}px` : undefined;
  const pct = (index: number) => (maxIndex <= 0 ? 0 : (index / maxIndex) * 100);

  // No drag handles: clicking a dot moves whichever endpoint is nearer, which
  // keeps from <= to and lets the colorized range communicate the selection
  // without the two stacked slider rows eating vertical space.
  const selectIndex = (index: number) => {
    if (!steps.length) {
      return;
    }

    const distFrom = Math.abs(index - fromIndex);
    const distTo = Math.abs(index - toIndex);
    const moveTo =
      index >= toIndex || (index > fromIndex && distTo <= distFrom);

    if (moveTo) {
      const step = steps[clamp(index, Math.min(1, maxIndex), maxIndex)];
      if (step?.id) {
        onToPatchsetChange(step.id);
      }
      return;
    }

    const step = steps[clamp(index, 0, maxIndex)];
    onFromPatchsetChange(step?.id || "");
  };

  const handleKeyDown = (
    index: number,
    event: KeyboardEvent<HTMLButtonElement>
  ) => {
    if (event.key === "ArrowLeft" || event.key === "ArrowDown") {
      event.preventDefault();
      selectIndex(clamp(index - 1, 0, maxIndex));
    } else if (event.key === "ArrowRight" || event.key === "ArrowUp") {
      event.preventDefault();
      selectIndex(clamp(index + 1, 0, maxIndex));
    }
  };

  if (!selectablePatchsets.length) {
    return (
      <p className="mt-2 rounded-md bg-slate-50 dark:bg-zinc-950 px-3 py-2 text-sm text-slate-600 dark:text-zinc-400">
        No patchsets returned.
      </p>
    );
  }

  return (
    <div className="mt-2">
      <div className="sr-only">
        <label>
          Diff base
          <select
            aria-label="Diff base"
            onChange={(event) => onFromPatchsetChange(event.target.value)}
            value={fromPatchset}
          >
            <option value="">Recorded base</option>
            {selectablePatchsets.map((patchset) => (
              <option key={`from-${patchsetKey(patchset)}`} value={patchset.id}>
                {patchsetOptionLabel(patchset)}
              </option>
            ))}
          </select>
        </label>
        <label>
          Target patchset
          <select
            aria-label="Target patchset"
            onChange={(event) => onToPatchsetChange(event.target.value)}
            value={toPatchset}
          >
            {selectablePatchsets.map((patchset) => (
              <option key={`to-${patchsetKey(patchset)}`} value={patchset.id}>
                {patchsetOptionLabel(patchset)}
              </option>
            ))}
          </select>
        </label>
      </div>

      <div className="relative h-10 px-4 md:px-5">
        <div
          className="relative mx-auto h-full"
          style={{ maxWidth: "100%", width: trackWidth }}
        >
          <div className="absolute inset-x-0 top-[7px] h-0.5 -translate-y-1/2 rounded-full bg-slate-200 dark:bg-zinc-700" />
          <div
            aria-hidden="true"
            className="absolute top-[7px] h-0.5 -translate-y-1/2 rounded-full bg-zinc-900"
            style={{
              left: `${pct(fromIndex)}%`,
              width: `${pct(toIndex) - pct(fromIndex)}%`
            }}
          />
          <div className="absolute inset-x-0 top-0 flex items-start justify-between">
            {steps.map((step, index) => {
              const isFrom = index === fromIndex;
              const isTo = index === toIndex;
              const isEndpoint = isFrom || isTo;
              const inRange = index > fromIndex && index < toIndex;
              const isCurrent =
                Boolean(currentPatchsetId) && step.id === currentPatchsetId;
              return (
                <button
                  aria-current={isTo ? "true" : undefined}
                  aria-label={
                    index === 0
                      ? "Use recorded base as diff base"
                      : `Compare ${patchsetOptionLabel(step.patchset)}`
                  }
                  className="group flex min-w-7 flex-col items-center gap-1 text-[10px] font-medium md:min-w-8"
                  key={step.id || "base"}
                  onClick={() => selectIndex(index)}
                  onKeyDown={(event) => handleKeyDown(index, event)}
                  type="button"
                >
                  <span
                    className={cn(
                      "rounded-full border-2 transition group-hover:border-zinc-950",
                      isEndpoint
                        ? "h-3.5 w-3.5 border-zinc-950 bg-zinc-950"
                        : inRange
                          ? "h-3 w-3 border-zinc-900 bg-zinc-900"
                          : "h-2.5 w-2.5 border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900",
                      isCurrent &&
                        !isEndpoint &&
                        !inRange &&
                        "border-emerald-500 ring-2 ring-emerald-100"
                    )}
                    title={isCurrent ? "Current patchset" : undefined}
                  />
                  <span
                    className={cn(
                      isEndpoint || inRange
                        ? "text-zinc-950 dark:text-zinc-50"
                        : "text-slate-500 dark:text-zinc-400",
                      isCurrent && !isEndpoint && !inRange && "text-emerald-700 dark:text-emerald-300"
                    )}
                  >
                    {step.label}
                  </span>
                </button>
              );
            })}
          </div>
        </div>
      </div>
    </div>
  );
}
