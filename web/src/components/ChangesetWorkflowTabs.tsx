import { cn } from "../lib/cn";
import { Surface } from "./ui";

type ChangesetWorkflowView = "changesets";

export function ChangesetWorkflowTabs({
  active
}: {
  active: ChangesetWorkflowView;
}) {
  return (
    <div className="mt-6">
      <Surface
        aria-label="Changeset workflow views"
        as="nav"
        className="flex max-w-full min-w-0 gap-1 overflow-x-auto p-1"
        level="low"
      >
        <span className={tabClass(active === "changesets")}>
          Changesets
        </span>
      </Surface>
    </div>
  );
}

function tabClass(active: boolean) {
  return cn(
    "shrink-0 rounded-sm px-3 py-2 font-label text-sm font-semibold transition",
    active
      ? "bg-surface-container-highest text-primary"
      : "text-on-surface-variant hover:bg-surface-container hover:text-primary"
  );
}
