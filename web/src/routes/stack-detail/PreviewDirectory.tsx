import type { TreeEntry } from "../../api/types";
import { parentRepositoryPath } from "./stackUtils";

export function PreviewDirectory({
  entries,
  onOpen,
  path
}: {
  entries: TreeEntry[];
  onOpen(path: string): void;
  path: string;
}) {
  const parentPath = parentRepositoryPath(path);

  return (
    <div className="divide-y divide-slate-200 border-t border-slate-200">
      {path !== "/" ? (
        <button
          className="flex w-full items-center justify-between px-4 py-3 text-left text-sm font-medium text-slate-700 transition hover:bg-slate-50"
          onClick={() => onOpen(parentPath)}
          type="button"
        >
          <span>Parent directory</span>
          <span className="font-mono text-xs text-slate-500">{parentPath}</span>
        </button>
      ) : null}
      {entries.length ? (
        entries.map((entry) => (
          <button
            className="flex w-full items-center justify-between gap-4 px-4 py-3 text-left text-sm transition hover:bg-slate-50"
            key={entry.path || entry.name}
            onClick={() => onOpen(entry.path || "/")}
            type="button"
          >
            <span className="min-w-0 break-all font-medium text-zinc-950">
              {entry.name || entry.path || "entry"}
            </span>
            <span className="shrink-0 rounded border border-slate-200 bg-slate-50 px-2 py-1 text-xs font-semibold text-slate-600">
              {entry.kind === "ENTRY_KIND_FILE" ? "file" : "directory"}
            </span>
          </button>
        ))
      ) : (
        <div className="px-4 py-6 text-sm text-slate-600">
          This preview directory is empty.
        </div>
      )}
    </div>
  );
}