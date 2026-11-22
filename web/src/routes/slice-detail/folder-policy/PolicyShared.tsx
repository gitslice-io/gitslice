import type { FolderCheckDefinition, FolderPolicyProblem } from "../../../api/types";
import { SliceNotice } from "../../../components/slices/SlicePageParts";
import { cn } from "../../../lib/cn";

export function ProblemNotices({ problems }: { problems: FolderPolicyProblem[] }) {
  if (problems.length === 0) {
    return null;
  }
  return (
    <div className="grid gap-2">
      {problems.map((p) => (
        <SliceNotice key={`${p.kind}:${p.file}`} title={`${p.file} does not parse`} tone="error">
          {p.message}
        </SliceNotice>
      ))}
    </div>
  );
}

export function InheritanceBadge({
  currentFolder,
  sourceFolder
}: {
  currentFolder: string;
  sourceFolder: string;
}) {
  if (!sourceFolder) {
    return null;
  }
  const inherited = sourceFolder !== currentFolder;
  return (
    <span
      className={cn(
        "shrink-0 rounded border px-1.5 py-px font-mono text-[11px] font-medium",
        inherited
          ? "border-slate-200 bg-slate-50 text-slate-600 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-300"
          : "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-900/60 dark:bg-emerald-950/40 dark:text-emerald-300"
      )}
    >
      {inherited ? `Inherited from ${sourceFolder}` : "Defined here"}
    </span>
  );
}

export function CheckCard({ check, folder }: { check: FolderCheckDefinition; folder: string }) {
  return (
    <div className="min-w-0 rounded-md border border-slate-200 px-3 py-2.5 dark:border-zinc-800">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
          <span className="break-all font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">{check.id}</span>
          {check.required ? (
            <span className="rounded border border-amber-300 bg-amber-50 px-1.5 py-px text-[11px] font-semibold text-amber-900 dark:border-amber-800/80 dark:bg-amber-950/50 dark:text-amber-200">
              Required
            </span>
          ) : null}
          {check.startedBy === "trigger" || check.startedBy === "automation" ? (
            <span className="rounded border border-slate-200 bg-slate-50 px-1.5 py-px text-[11px] font-medium text-slate-600 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-300">
              Automation-started
            </span>
          ) : null}
        </div>
        <InheritanceBadge currentFolder={folder} sourceFolder={check.folder ?? ""} />
      </div>
      {check.run ? (
        <pre className="mt-1.5 max-w-full overflow-x-auto whitespace-pre rounded bg-slate-50 px-2 py-1.5 font-mono text-xs text-slate-600 dark:bg-zinc-950/70 dark:text-zinc-400">
          {check.run}
        </pre>
      ) : null}
      {(check.paths ?? []).length > 0 ? (
        <p className="mt-1.5 [overflow-wrap:anywhere] font-mono text-[11px] text-slate-500 dark:text-zinc-400">
          paths {(check.paths ?? []).join(" ")}
        </p>
      ) : null}
    </div>
  );
}
