import { useMemo, useState } from "react";

import { cn } from "../../lib/cn";
import { detectDelimiter, parseDelimited } from "../../lib/delimited";

// A table preview for CSV and TSV files. Large files show the first rows and
// reveal more on request, so a big export never freezes the page.

const MAX_PARSED_ROWS = 100_000;
const ROWS_PER_PAGE = 200;

const NUMBER_PATTERN = /^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?%?$/;

export function TableViewer({ path, source }: { path: string; source: string }) {
  const parsed = useMemo(() => {
    const delimiter = detectDelimiter(path, source);
    return { delimiter, ...parseDelimited(source, delimiter, MAX_PARSED_ROWS) };
  }, [path, source]);
  const [headerOverride, setHeaderOverride] = useState<boolean | null>(null);
  const [visible, setVisible] = useState(ROWS_PER_PAGE);

  const { rows } = parsed;
  const columnCount = useMemo(
    () => rows.reduce((widest, row) => Math.max(widest, row.length), 0),
    [rows]
  );
  const hasHeader = headerOverride ?? looksLikeHeader(rows);
  const header = hasHeader ? rows[0] : undefined;
  const body = hasHeader ? rows.slice(1) : rows;
  const numeric = useMemo(() => numericColumns(body, columnCount), [body, columnCount]);

  if (rows.length === 0) {
    return <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">This file is empty.</p>;
  }

  const shown = body.slice(0, visible);
  const total = `${body.length.toLocaleString()}${parsed.truncated ? "+" : ""}`;

  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
        <span>
          {total} {body.length === 1 && !parsed.truncated ? "row" : "rows"} · {columnCount}{" "}
          {columnCount === 1 ? "column" : "columns"} · separated by {delimiterName(parsed.delimiter)}
        </span>
        <label className="inline-flex items-center gap-2">
          <input
            checked={hasHeader}
            onChange={(event) => setHeaderOverride(event.target.checked)}
            type="checkbox"
          />
          First row is a header
        </label>
      </div>
      <div className="overflow-auto">
        <table className="min-w-full border-collapse text-sm">
          {header ? (
            <thead className="sticky top-0 z-10 bg-slate-50 dark:bg-zinc-950">
              <tr>
                <th className={cn(INDEX_CELL, "border-b")} scope="col" />
                {padded(header, columnCount).map((cell, column) => (
                  <th
                    className={cn(
                      "border-b border-slate-200 px-3 py-2 font-semibold text-zinc-900 dark:border-zinc-800 dark:text-zinc-50",
                      numeric[column] ? "text-right" : "text-left"
                    )}
                    key={column}
                    scope="col"
                    title={cell}
                  >
                    {cell}
                  </th>
                ))}
              </tr>
            </thead>
          ) : null}
          <tbody>
            {shown.map((row, index) => (
              <tr
                className="odd:bg-white even:bg-slate-50/60 dark:odd:bg-zinc-900 dark:even:bg-zinc-950/50"
                key={index}
              >
                <th className={INDEX_CELL} scope="row">
                  {index + 1}
                </th>
                {padded(row, columnCount).map((cell, column) => (
                  <td
                    className={cn(
                      "max-w-[28rem] truncate whitespace-nowrap border-slate-100 px-3 py-1.5 text-zinc-900 dark:border-zinc-800 dark:text-zinc-100",
                      numeric[column] ? "text-right tabular-nums" : "text-left"
                    )}
                    key={column}
                    title={cell}
                  >
                    {cell}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {shown.length < body.length ? (
        <div className="flex items-center justify-between gap-3 border-t border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
          <span>
            Showing {shown.length.toLocaleString()} of {total} rows
          </span>
          <button
            className="rounded-md border border-slate-200 px-2.5 py-1 font-medium text-slate-700 transition hover:text-zinc-950 dark:border-zinc-800 dark:text-zinc-300 dark:hover:text-zinc-50"
            onClick={() => setVisible((count) => count + ROWS_PER_PAGE * 5)}
            type="button"
          >
            Show more
          </button>
        </div>
      ) : parsed.truncated ? (
        <p className="border-t border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
          Only the first {MAX_PARSED_ROWS.toLocaleString()} rows are previewed. Switch to Raw for the whole file.
        </p>
      ) : null}
    </div>
  );
}

const INDEX_CELL =
  "sticky left-0 w-10 select-none border-slate-200 bg-slate-50 px-2 py-1.5 text-right text-xs font-normal text-slate-400 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-500";

function padded(row: string[], width: number) {
  return row.length >= width ? row : [...row, ...Array<string>(width - row.length).fill("")];
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

// A first row is taken as a header when every cell is text, none is a number,
// and some later row exists. It is a guess; the checkbox overrides it.
export function looksLikeHeader(rows: string[][]) {
  if (rows.length < 2) {
    return false;
  }
  const first = rows[0];
  return first.some((cell) => cell.trim() !== "") && first.every((cell) => !NUMBER_PATTERN.test(cell.trim()));
}

// numericColumns marks the columns whose non-empty cells (in a sample) are all
// numbers, which are right-aligned.
export function numericColumns(rows: string[][], columns: number) {
  const result: boolean[] = [];
  const sample = rows.slice(0, 500);
  for (let column = 0; column < columns; column++) {
    let seen = 0;
    let numbers = 0;
    for (const row of sample) {
      const cell = (row[column] ?? "").trim();
      if (cell === "") {
        continue;
      }
      seen++;
      if (NUMBER_PATTERN.test(cell)) {
        numbers++;
      }
    }
    result.push(seen > 0 && numbers === seen);
  }
  return result;
}
