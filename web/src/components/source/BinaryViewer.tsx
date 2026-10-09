import { useMobileDetailsClass } from "../MobileDetails";
import { MediaViewer } from "./MediaViewer";
import { ParquetViewer } from "./ParquetViewer";
import { SheetViewer } from "./SheetViewer";
import type { BinaryKind } from "./sourceUtils";

const LABELS: Record<BinaryKind, string> = {
  audio: "Audio",
  parquet: "Parquet",
  pdf: "PDF",
  spreadsheet: "Spreadsheet",
  video: "Video"
};

// BinaryViewer shows files that have no useful text form: media, documents and
// data files, from the file's bytes.
export function BinaryViewer({ data, kind, path }: { data: string; kind: BinaryKind; path: string }) {
  const foldedOnPhone = useMobileDetailsClass();
  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
      <div className={`flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 bg-slate-50 px-4 py-3 text-xs text-slate-500 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-400 ${foldedOnPhone}`}>
        <div className="min-w-0 truncate font-mono text-slate-600 dark:text-zinc-400">{path}</div>
        <span>{LABELS[kind]}</span>
      </div>
      {kind === "spreadsheet" ? (
        <SheetViewer data={data} />
      ) : kind === "parquet" ? (
        <ParquetViewer data={data} />
      ) : (
        <MediaViewer data={data} kind={kind} path={path} />
      )}
    </div>
  );
}
