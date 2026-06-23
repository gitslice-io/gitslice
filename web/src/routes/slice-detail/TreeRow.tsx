import { FileTypeIcon } from "../../components/source/FileTypeIcon";
import { FolderGlyph } from "./FolderGlyph";

interface TreeRowProps {
  depth: number;
  isExpanded: boolean;
  isLoading: boolean;
  node: import("./sourceTree").SourceTreeNode;
  onSelect(path: string): void;
  onToggle(path: string): void;
  selectedPath: string;
}

export function TreeRow({
  depth,
  isExpanded,
  isLoading,
  node,
  onSelect,
  onToggle,
  selectedPath
}: TreeRowProps) {
  const isDirectory = node.kind === "ENTRY_KIND_DIRECTORY";
  const isActive = selectedPath === node.path;
  const buttonLabel = node.path || "slice root";

  return (
    <div>
      <div
        className={[
          "group flex h-8 min-w-0 items-center gap-1.5 pr-2 text-sm transition",
          isActive
            ? "bg-slate-100 text-zinc-950"
            : "text-slate-700 hover:bg-slate-50 hover:text-zinc-950"
        ].join(" ")}
        style={{ paddingLeft: `${depth * 14 + 8}px` }}
      >
        {isDirectory ? (
          <button
            aria-expanded={isExpanded}
            aria-label={`${isExpanded ? "Collapse" : "Expand"} ${buttonLabel}`}
            className="flex h-5 w-5 shrink-0 items-center justify-center rounded text-slate-500 transition hover:bg-slate-200 hover:text-zinc-950"
            onClick={() => onToggle(node.path)}
            type="button"
          >
            <span
              className={[
                "h-0 w-0 border-y-[4px] border-l-[5px] border-y-transparent border-l-current transition-transform",
                isExpanded ? "rotate-90" : ""
              ].join(" ")}
            />
          </button>
        ) : (
          <span className="h-5 w-5 shrink-0" />
        )}

        <button
          className="flex min-w-0 flex-1 items-center gap-2 py-1 text-left"
          onClick={() => onSelect(node.path)}
          title={buttonLabel}
          type="button"
        >
          {isDirectory ? <FolderGlyph /> : <FileTypeIcon name={node.name} />}
          <span
            className={[
              "truncate",
              isActive ? "font-semibold" : "font-medium",
              node.synthetic ? "text-slate-600" : ""
            ].join(" ")}
          >
            {node.name}
          </span>
        </button>

        {isLoading ? (
          <span className="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-slate-400" />
        ) : null}
      </div>
    </div>
  );
}