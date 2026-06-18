import { Link } from "@tanstack/react-router";

import { cn } from "../lib/cn";

type ChangesetWorkflowView = "changesets" | "stacks";

export function ChangesetWorkflowTabs({
  active,
  sliceLabel
}: {
  active: ChangesetWorkflowView;
  sliceLabel?: string;
}) {
  const sliceSearch = sliceLabel ? ({ slice: sliceLabel } as never) : undefined;

  return (
    <div className="mt-6 border-b border-slate-200">
      <nav
        aria-label="Changeset workflow views"
        className="-mb-px flex min-w-0 gap-1 overflow-x-auto"
      >
        <Link
          className={tabClass(active === "changesets")}
          search={sliceSearch}
          to="/changesets"
        >
          Changesets
        </Link>
        <Link
          className={tabClass(active === "stacks")}
          search={sliceSearch}
          to="/stacks"
        >
          Stacks
        </Link>
      </nav>
    </div>
  );
}

function tabClass(active: boolean) {
  return cn(
    "shrink-0 border-b-2 px-3 py-2 text-sm font-semibold transition",
    active
      ? "border-zinc-950 text-zinc-950"
      : "border-transparent text-slate-600 hover:border-slate-300 hover:text-zinc-950"
  );
}
