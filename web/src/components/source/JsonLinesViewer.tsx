import { useMemo, useState } from "react";

import { cn } from "../../lib/cn";
import { DataTable, cellText } from "./DataTable";
import { JsonTree } from "./JsonTree";

// JSON Lines (one JSON value per line): agent traces, exports and logs. The
// table view has a column per top-level key; the records view opens each one.

const MAX_RECORDS = 50_000;
const MAX_COLUMNS = 40;

type Mode = "table" | "records";

export function parseJsonLines(source: string, limit = MAX_RECORDS) {
  const records: unknown[] = [];
  const bad: number[] = [];
  const lines = source.split(/\r\n|\r|\n/);
  let truncated = false;
  for (let index = 0; index < lines.length; index++) {
    const line = lines[index].trim();
    if (line === "") {
      continue;
    }
    if (records.length >= limit) {
      truncated = true;
      break;
    }
    try {
      records.push(JSON.parse(line));
    } catch {
      bad.push(index + 1);
    }
  }
  return { bad, records, truncated };
}

export function JsonLinesViewer({ source }: { source: string }) {
  const parsed = useMemo(() => parseJsonLines(source), [source]);
  const [mode, setMode] = useState<Mode>("table");
  const table = useMemo(() => recordsToTable(parsed.records), [parsed.records]);

  if (parsed.records.length === 0 && parsed.bad.length === 0) {
    return <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">This file is empty.</p>;
  }

  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
        <div className="inline-flex overflow-hidden rounded-md border border-slate-200 dark:border-zinc-800">
          {(["table", "records"] as const).map((value) => (
            <button
              aria-pressed={mode === value}
              className={cn(
                "px-2.5 py-1 font-medium capitalize",
                mode === value ? "bg-slate-100 text-zinc-950 dark:bg-zinc-800 dark:text-zinc-50" : "text-slate-600 dark:text-zinc-400"
              )}
              key={value}
              onClick={() => setMode(value)}
              type="button"
            >
              {value}
            </button>
          ))}
        </div>
        {parsed.bad.length > 0 ? (
          <span className="text-amber-700 dark:text-amber-300">
            {parsed.bad.length} line{parsed.bad.length === 1 ? "" : "s"} could not be parsed (first: line {parsed.bad[0]})
          </span>
        ) : null}
      </div>
      {mode === "table" ? (
        <DataTable
          header={table.header}
          limitNote={`Only the first ${MAX_RECORDS.toLocaleString()} records are previewed. Switch to Raw for the whole file.`}
          rows={table.rows}
          summary={table.droppedColumns > 0 ? `${table.droppedColumns} more keys not shown` : "one record per line"}
          truncated={parsed.truncated}
        />
      ) : (
        <JsonTree defaultDepth={1} value={parsed.records} />
      )}
    </div>
  );
}

export function recordsToTable(records: unknown[]) {
  const columns: string[] = [];
  const seen = new Set<string>();
  let dropped = 0;
  const hasScalarRecords = records.some((record) => record === null || typeof record !== "object" || Array.isArray(record));
  for (const record of records) {
    if (record && typeof record === "object" && !Array.isArray(record)) {
      for (const key of Object.keys(record)) {
        if (!seen.has(key)) {
          seen.add(key);
          if (columns.length < MAX_COLUMNS) {
            columns.push(key);
          } else {
            dropped++;
          }
        }
      }
    }
  }
  // Records that are not objects (numbers, strings, arrays) go in a column of their own.
  if (hasScalarRecords && !seen.has("value")) {
    columns.unshift("value");
  }
  const rows = records.map((record) => {
    if (record && typeof record === "object" && !Array.isArray(record)) {
      const object = record as Record<string, unknown>;
      return columns.map((column) => cellText(object[column]));
    }
    return columns.map((column) => (column === "value" ? cellText(record) : ""));
  });
  return { droppedColumns: dropped, header: columns, rows };
}
