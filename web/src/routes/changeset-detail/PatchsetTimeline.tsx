import {
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent
} from "react";

import type { Patchset } from "../../api/types";
import { cn } from "../../lib/cn";
import {
  TimelineHandle,
  TimelineStep,
  clamp,
  findPatchset,
  handleTransform,
  patchsetDotLabel,
  patchsetKey,
  patchsetOptionLabel,
  timelineIndexForValue,
  timelinePosition
} from "./patchsetUtils";
import { TimelineHandleButton } from "./TimelineHandleButton";

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
  const trackRef = useRef<HTMLDivElement | null>(null);
  const [dragging, setDragging] = useState<TimelineHandle | null>(null);
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
  // edge-to-edge slider for two dots looks broken. The track only grows to fill
  // the container once there are enough patchsets to need the room.
  const STEP_GAP_PX = 104;
  const trackWidth = maxIndex > 0 ? `${maxIndex * STEP_GAP_PX}px` : undefined;

  const applyIndex = (handle: TimelineHandle, index: number) => {
    if (!steps.length) {
      return;
    }

    const nextIndex =
      handle === "from"
        ? clamp(index, 0, maxIndex)
        : clamp(index, Math.min(1, maxIndex), maxIndex);
    const step = steps[nextIndex];

    if (handle === "from") {
      onFromPatchsetChange(step?.id || "");
      return;
    }

    if (step?.id) {
      onToPatchsetChange(step.id);
    }
  };

  const indexForPointer = (clientX: number) => {
    const track = trackRef.current;
    if (!track || maxIndex === 0) {
      return 0;
    }

    const rect = track.getBoundingClientRect();
    const ratio = clamp((clientX - rect.left) / rect.width, 0, 1);
    return Math.round(ratio * maxIndex);
  };

  const handlePointerDown = (
    handle: TimelineHandle,
    event: PointerEvent<HTMLButtonElement>
  ) => {
    event.preventDefault();
    event.currentTarget.setPointerCapture(event.pointerId);
    setDragging(handle);
    applyIndex(handle, indexForPointer(event.clientX));
  };

  const handlePointerMove = (event: PointerEvent<HTMLButtonElement>) => {
    if (!dragging) {
      return;
    }
    applyIndex(dragging, indexForPointer(event.clientX));
  };

  const stopDragging = (event: PointerEvent<HTMLButtonElement>) => {
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    setDragging(null);
  };

  const handleKeyDown = (
    handle: TimelineHandle,
    currentIndex: number,
    event: KeyboardEvent<HTMLButtonElement>
  ) => {
    if (event.key === "ArrowLeft" || event.key === "ArrowDown") {
      event.preventDefault();
      applyIndex(handle, currentIndex - 1);
    } else if (event.key === "ArrowRight" || event.key === "ArrowUp") {
      event.preventDefault();
      applyIndex(handle, currentIndex + 1);
    } else if (event.key === "Home") {
      event.preventDefault();
      applyIndex(handle, handle === "from" ? 0 : 1);
    } else if (event.key === "End") {
      event.preventDefault();
      applyIndex(handle, maxIndex);
    }
  };

  if (!selectablePatchsets.length) {
    return (
      <p className="mt-2 rounded-md bg-slate-50 px-3 py-2 text-sm text-slate-600">
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

      <div className="relative h-20 px-4 md:px-5">
        <div
          className="relative mx-auto h-full"
          ref={trackRef}
          style={{ maxWidth: "100%", width: trackWidth }}
        >
          <div className="absolute inset-x-0 top-9 h-px bg-slate-300" />
          <div className="absolute inset-x-0 top-6 flex items-center justify-between">
            {steps.map((step, index) => {
              const isCurrent =
                currentPatchsetId && step.id === currentPatchsetId;
              return (
                <button
                  aria-current={index === toIndex ? "true" : undefined}
                  aria-label={
                    index === 0
                      ? "Use recorded base as diff base"
                      : `Compare to ${patchsetOptionLabel(step.patchset)}`
                  }
                  className="group flex min-w-7 -translate-y-0.5 flex-col items-center gap-1 text-[10px] font-medium text-slate-500 md:min-w-8"
                  key={step.id || "base"}
                  onClick={() =>
                    index === 0
                      ? onFromPatchsetChange("")
                      : onToPatchsetChange(step.id)
                  }
                  type="button"
                >
                  <span
                    className={cn(
                      "rounded-full border-2 bg-white transition group-hover:border-zinc-950",
                      index === fromIndex || index === toIndex
                        ? "h-3.5 w-3.5 border-zinc-950"
                        : "h-3 w-3 border-slate-300",
                      isCurrent &&
                        !(index === fromIndex || index === toIndex) &&
                        "border-emerald-500 ring-2 ring-emerald-100"
                    )}
                    title={isCurrent ? "Current patchset" : undefined}
                  />
                  <span
                    className={cn(
                      isCurrent &&
                        !(index === fromIndex || index === toIndex) &&
                        "text-emerald-700"
                    )}
                  >
                    {step.label}
                  </span>
                </button>
              );
            })}
          </div>

          <TimelineHandleButton
            handle="from"
            index={fromIndex}
            label={
              fromIndex === 0 ? "From Base" : `From ${steps[fromIndex]?.label}`
            }
            maxIndex={maxIndex}
            onKeyDown={handleKeyDown}
            onPointerDown={handlePointerDown}
            onPointerMove={handlePointerMove}
            onPointerUp={stopDragging}
            topClassName="top-0"
          />
          <TimelineHandleButton
            handle="to"
            index={toIndex}
            label={`To ${steps[toIndex]?.label || ""}`}
            maxIndex={maxIndex}
            onKeyDown={handleKeyDown}
            onPointerDown={handlePointerDown}
            onPointerMove={handlePointerMove}
            onPointerUp={stopDragging}
            topClassName="top-12"
          />
        </div>
      </div>
    </div>
  );
}