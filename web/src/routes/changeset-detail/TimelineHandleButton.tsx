import { type KeyboardEvent, type PointerEvent } from "react";

import { cn } from "../../lib/cn";
import { TimelineHandle, handleTransform, timelinePosition } from "./patchsetUtils";

export function TimelineHandleButton({
  handle,
  index,
  label,
  maxIndex,
  onKeyDown,
  onPointerDown,
  onPointerMove,
  onPointerUp,
  topClassName
}: {
  handle: TimelineHandle;
  index: number;
  label: string;
  maxIndex: number;
  onKeyDown(
    handle: TimelineHandle,
    currentIndex: number,
    event: KeyboardEvent<HTMLButtonElement>
  ): void;
  onPointerDown(
    handle: TimelineHandle,
    event: PointerEvent<HTMLButtonElement>
  ): void;
  onPointerMove(event: PointerEvent<HTMLButtonElement>): void;
  onPointerUp(event: PointerEvent<HTMLButtonElement>): void;
  topClassName: string;
}) {
  return (
    <button
      aria-label={`Drag ${handle} patchset handle`}
      aria-valuemax={maxIndex}
      aria-valuemin={handle === "from" ? 0 : Math.min(1, maxIndex)}
      aria-valuenow={index}
      className={cn(
        "absolute rounded-full border px-2 py-1 text-[11px] font-semibold shadow-sm transition active:scale-[0.98]",
        handle === "from"
          ? "border-slate-300 bg-white text-slate-700"
          : "border-zinc-950 bg-zinc-950 text-white",
        topClassName
      )}
      onKeyDown={(event) => onKeyDown(handle, index, event)}
      onPointerDown={(event) => onPointerDown(handle, event)}
      onPointerCancel={onPointerUp}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      role="slider"
      style={{
        left: timelinePosition(index, maxIndex),
        touchAction: "none",
        transform: handleTransform(index, maxIndex)
      }}
      type="button"
    >
      {label}
    </button>
  );
}