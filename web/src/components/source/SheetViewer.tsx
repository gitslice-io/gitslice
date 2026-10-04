import { useEffect, useState } from "react";

import { cn } from "../../lib/cn";
import { useBytes } from "../../lib/bytes";
import { DataTable, cellText } from "./DataTable";

// An Excel workbook (.xlsx), one tab per sheet, read in the browser. The reader
// loads on first use. Older .xls files are not supported.

interface Sheet {
  name: string;
  rows: string[][];
}

type State = { status: "loading" } | { status: "error"; message: string } | { status: "ready"; sheets: Sheet[] };

const MAX_ROWS = 100_000;

export function SheetViewer({ data }: { data: string }) {
  const bytes = useBytes(data);
  const [state, setState] = useState<State>({ status: "loading" });
  const [active, setActive] = useState(0);

  useEffect(() => {
    let current = true;
    if (!bytes) {
      setState({ status: "error", message: "the file could not be decoded" });
      return;
    }
    setState({ status: "loading" });
    setActive(0);
    (async () => {
      const { default: readXlsxFile } = await import("read-excel-file/universal");
      const copy = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer;
      const sheets = await readXlsxFile(copy);
      return sheets.map((sheet) => ({
        name: sheet.sheet,
        rows: sheet.data.slice(0, MAX_ROWS + 1).map((row) => row.map((cell) => cellText(cell)))
      }));
    })().then(
      (sheets) => current && setState({ status: "ready", sheets }),
      (error) => current && setState({ status: "error", message: error instanceof Error ? error.message : "unreadable workbook" })
    );
    return () => {
      current = false;
    };
  }, [bytes]);

  if (state.status === "loading") {
    return <p className="p-4 text-sm text-slate-500 dark:text-zinc-400" role="status">Reading the workbook…</p>;
  }
  if (state.status === "error") {
    return (
      <p className="bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200" role="alert">
        The workbook could not be read: {state.message}. Only .xlsx files are previewed.
      </p>
    );
  }
  const sheet = state.sheets[Math.min(active, state.sheets.length - 1)];
  if (!sheet) {
    return <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">This workbook has no sheets.</p>;
  }

  return (
    <div>
      <div className="flex gap-1 overflow-x-auto border-b border-slate-200 px-4 pt-2 dark:border-zinc-800" role="tablist">
        {state.sheets.map((candidate, index) => (
          <button
            aria-selected={index === active}
            className={cn(
              "whitespace-nowrap rounded-t-md border border-b-0 px-3 py-1.5 text-xs font-medium",
              index === active
                ? "border-slate-200 bg-white text-zinc-950 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-50"
                : "border-transparent text-slate-500 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
            )}
            key={`${candidate.name}-${index}`}
            onClick={() => setActive(index)}
            role="tab"
            type="button"
          >
            {candidate.name}
          </button>
        ))}
      </div>
      <DataTable
        canToggleHeader
        key={`${sheet.name}-${active}`}
        limitNote={`Only the first ${MAX_ROWS.toLocaleString()} rows are previewed.`}
        rows={sheet.rows.slice(0, MAX_ROWS)}
        summary={`sheet “${sheet.name}”`}
        truncated={sheet.rows.length > MAX_ROWS}
      />
    </div>
  );
}
