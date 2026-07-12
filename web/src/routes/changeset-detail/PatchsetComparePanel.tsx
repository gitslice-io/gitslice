import { useEffect, useRef, useState } from "react";
import type { Patchset } from "../../api/types";
import { cn } from "../../lib/cn";
import { findPatchset, patchsetOptionLabel } from "./patchsetUtils";
import { PatchsetTimeline } from "./PatchsetTimeline";

export function PatchsetComparePanel({
  conversationOpen,
  currentPatchsetId,
  fromPatchset,
  onFromPatchsetChange,
  onToPatchsetChange,
  onToggleConversation,
  patchsets,
  toPatchset
}: {
  conversationOpen: boolean;
  currentPatchsetId?: string;
  fromPatchset: string;
  onFromPatchsetChange(value: string): void;
  onToPatchsetChange(value: string): void;
  onToggleConversation(): void;
  patchsets: Patchset[];
  toPatchset: string;
}) {
  // The timeline lives behind the summary on every breakpoint: collapsed by
  // default so it stops eating vertical space, opened on demand. On mobile it
  // expands inline; on desktop it floats as a popover anchored under the bar.
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const panelRef = useRef<HTMLDivElement | null>(null);
  const fromLabel = fromPatchset
    ? patchsetOptionLabel(findPatchset(patchsets, fromPatchset))
    : "Recorded base";
  const toLabel = patchsetOptionLabel(findPatchset(patchsets, toPatchset));

  useEffect(() => {
    if (!open) {
      return;
    }

    const onPointerDown = (event: globalThis.MouseEvent) => {
      const target = event.target;
      if (
        target instanceof Node &&
        !triggerRef.current?.contains(target) &&
        !panelRef.current?.contains(target)
      ) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };
    const onReflow = () => setOpen(false);

    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener("resize", onReflow);
    window.addEventListener("scroll", onReflow, true);

    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("resize", onReflow);
      window.removeEventListener("scroll", onReflow, true);
    };
  }, [open]);

  return (
    <div className="relative">
      <div className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <h2 className="text-[11px] font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400 md:text-xs">
            Patchsets
          </h2>
          <button
            aria-controls="patchset-timeline"
            aria-expanded={open}
            aria-haspopup="true"
            className="-mx-1 inline-flex min-w-0 items-center gap-1.5 rounded px-1 text-left transition hover:bg-slate-50 dark:hover:bg-zinc-950"
            onClick={() => setOpen((value) => !value)}
            ref={triggerRef}
            type="button"
          >
            <span className="min-w-0 truncate text-[11px] font-medium text-zinc-950 dark:text-zinc-50 md:text-xs">
              <span className="text-slate-500 dark:text-zinc-400">{fromLabel}</span>
              <span className="mx-1 text-slate-400 dark:text-zinc-500">→</span>
              <span>{toLabel || "selected patchset"}</span>
            </span>
            <span
              aria-hidden="true"
              className={cn(
                "inline-block shrink-0 text-[10px] text-slate-400 dark:text-zinc-500 transition-transform",
                open && "rotate-180"
              )}
            >
              ▾
            </span>
          </button>
        </div>
        <button
          aria-expanded={conversationOpen}
          aria-label="Toggle conversation for the selected patchset"
          className={cn(
            "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md border px-2 text-[11px] font-medium shadow-sm transition active:scale-[0.98] md:text-xs",
            conversationOpen
              ? "border-zinc-900 dark:border-zinc-100 bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950 hover:bg-zinc-800 dark:hover:bg-white"
              : "border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-700 dark:text-zinc-300 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50"
          )}
          onClick={onToggleConversation}
          type="button"
        >
          <span aria-hidden="true">💬</span>
          <span className="hidden sm:inline">Conversation</span>
        </button>
      </div>
      <div
        className={cn(
          "mt-2 lg:absolute lg:left-0 lg:right-0 lg:top-full lg:z-30 lg:mt-1 lg:rounded-md lg:border lg:border-slate-200 dark:lg:border-zinc-800 lg:bg-white dark:lg:bg-zinc-900 lg:p-3 lg:shadow-lg lg:shadow-slate-900/10",
          open ? "block" : "hidden"
        )}
        id="patchset-timeline"
        ref={panelRef}
      >
        <PatchsetTimeline
          currentPatchsetId={currentPatchsetId}
          fromPatchset={fromPatchset}
          onFromPatchsetChange={onFromPatchsetChange}
          onToPatchsetChange={onToPatchsetChange}
          patchsets={patchsets}
          toPatchset={toPatchset}
        />
      </div>
    </div>
  );
}
