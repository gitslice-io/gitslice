import { useState } from "react";
import type { Patchset } from "../../api/types";
import { cn } from "../../lib/cn";
import { findPatchset, patchsetOptionLabel } from "./patchsetUtils";
import { PatchsetTimeline } from "./PatchsetTimeline";

export function PatchsetComparePanel({
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
  const [mobileOpen, setMobileOpen] = useState(false);
  const fromLabel = fromPatchset
    ? patchsetOptionLabel(findPatchset(patchsets, fromPatchset))
    : "Recorded base";
  const toLabel = patchsetOptionLabel(findPatchset(patchsets, toPatchset));

  return (
    <section className="mt-2.5 rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 md:mt-3">
      <div className="flex items-center justify-between gap-2 px-3 py-2.5 md:px-5 md:py-3">
        <button
          aria-controls="patchset-timeline"
          aria-expanded={mobileOpen}
          className="-mx-1 flex min-w-0 flex-1 items-center gap-2 rounded px-1 text-left lg:cursor-default lg:pointer-events-none"
          onClick={() => setMobileOpen((value) => !value)}
          type="button"
        >
          <h2 className="text-[11px] font-semibold uppercase tracking-normal text-slate-500 md:text-xs">
            Patchsets
          </h2>
          <span
            aria-hidden="true"
            className={cn(
              "inline-block shrink-0 text-[10px] text-slate-400 transition-transform lg:hidden",
              mobileOpen && "rotate-90"
            )}
          >
            ▶
          </span>
          <p className="min-w-0 truncate text-[11px] font-medium text-zinc-950 md:text-xs">
            <span className="text-slate-500">{fromLabel}</span>
            <span className="mx-1 text-slate-400">→</span>
            <span>{toLabel || "selected patchset"}</span>
          </p>
        </button>
      </div>
      <div
        className={cn(
          "px-3 pb-2 md:px-5 md:pb-3",
          mobileOpen ? "block" : "hidden lg:block"
        )}
        id="patchset-timeline"
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
    </section>
  );
}