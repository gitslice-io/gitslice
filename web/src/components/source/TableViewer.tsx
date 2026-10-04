import { useMemo } from "react";

import { detectDelimiter, parseDelimited } from "../../lib/delimited";
import { DataTable } from "./DataTable";

const MAX_PARSED_ROWS = 100_000;

// A table preview for CSV and TSV files.
export function TableViewer({ path, source }: { path: string; source: string }) {
  const parsed = useMemo(() => {
    const delimiter = detectDelimiter(path, source);
    return { delimiter, ...parseDelimited(source, delimiter, MAX_PARSED_ROWS) };
  }, [path, source]);

  if (parsed.rows.length === 0) {
    return <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">This file is empty.</p>;
  }

  return (
    <DataTable
      canToggleHeader
      limitNote={`Only the first ${MAX_PARSED_ROWS.toLocaleString()} rows are previewed. Switch to Raw for the whole file.`}
      rows={parsed.rows}
      summary={`separated by ${delimiterName(parsed.delimiter)}`}
      truncated={parsed.truncated}
    />
  );
}

function delimiterName(delimiter: string) {
  switch (delimiter) {
    case "\t":
      return "tabs";
    case ";":
      return "semicolons";
    case "|":
      return "pipes";
    default:
      return "commas";
  }
}
