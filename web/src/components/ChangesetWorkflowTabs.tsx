import { cn } from "../lib/cn";

type ChangesetWorkflowView = "changesets";

export function ChangesetWorkflowTabs({
  active
}: {
  active: ChangesetWorkflowView;
}) {
  return (
    <div className="mt-6 border-b border-slate-200 dark:border-zinc-800">
      <nav
        aria-label="Changeset workflow views"
        className="-mb-px flex min-w-0 gap-1 overflow-x-auto"
      >
        <span className={tabClass(active === "changesets")}>
          Changesets
        </span>
      </nav>
    </div>
  );
}

function tabClass(active: boolean) {
  return cn(
    "shrink-0 border-b-2 px-3 py-2 text-sm font-semibold transition",
    active
      ? "border-zinc-950 text-zinc-950 dark:text-zinc-50"
      : "border-transparent text-slate-600 dark:text-zinc-400 hover:border-slate-300 dark:hover:border-zinc-700 hover:text-zinc-950 dark:hover:text-zinc-50"
  );
}
