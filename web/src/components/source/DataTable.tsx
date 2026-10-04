import { useMemo, useState } from "react";

import { cn } from "../../lib/cn";

// A table for rows of text: CSV and TSV files, spreadsheets, Parquet files and
// JSON Lines records all end up here. Large inputs show the first rows and
// reveal more on request, so a big export never freezes the page.

const ROWS_PER_PAGE = 200;

const NUMBER_PATTERN = /^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?%?$/;

interface DataTableProps {
  // Offer a checkbox that treats the first row as the header, guessing the
  // initial state. Without it the table has the header given, or none.
  canToggleHeader?: boolean;
  header?: string[];
  // Where the rows stop being the whole file (a parse limit was hit).
  limitNote?: string;
  rows: string[][];
  // What follows the row and column counts, such as "separated by commas".
  summary?: string;
  truncated?: boolean;
}

export function DataTable({
  canToggleHeader = false,
  header: givenHeader,
  limitNote,
  rows,
  summary,
  truncated = false
}: DataTableProps) {
  const [headerOverride, setHeaderOverride] = useState<boolean | null>(null);
  const [visible, setVisible] = useState(ROWS_PER_PAGE);

  const firstRowIsHeader = canToggleHeader && (headerOverride ?? looksLikeHeader(rows));
  const header = firstRowIsHeader ? rows[0] : givenHeader;
  const body = firstRowIsHeader ? rows.slice(1) : rows;
  const columnCount = useMemo(
    () => rows.reduce((widest, row) => Math.max(widest, row.length), header?.length ?? 0),
    [rows, header]
  );
  const numeric = useMemo(() => numericColumns(body, columnCount), [body, columnCount]);

  if (rows.length === 0 && !header) {
    return <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">There are no rows to show.</p>;
  }

  const shown = body.slice(0, visible);
  const total = `${body.length.toLocaleString()}${truncated ? "+" : ""}`;

  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
        <span>
          {total} {body.length === 1 && !truncated ? "row" : "rows"} · {columnCount}{" "}
          {columnCount === 1 ? "column" : "columns"}
          {summary ? ` · ${summary}` : ""}
        </span>
        {canToggleHeader ? (
          <label className="inline-flex items-center gap-2">
            <input
              checked={firstRowIsHeader}
              onChange={(event) => setHeaderOverride(event.target.checked)}
              type="checkbox"
            />
            First row is a header
          </label>
        ) : null}
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
      ) : truncated && limitNote ? (
        <p className="border-t border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
          {limitNote}
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

// A first row is taken as a header when some cell has text, none is a number,
// and a later row exists. It is a guess; the checkbox overrides it.
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

// cellText turns any parsed value into the text a cell shows.
export function cellText(value: unknown): string {
  if (value === null || value === undefined) {
    return "";
  }
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "bigint") {
    return value.toString();
  }
  if (value instanceof Date) {
    return Number.isNaN(value.getTime()) ? "" : value.toISOString();
  }
  if (value instanceof Uint8Array) {
    return `<${value.length} bytes>`;
  }
  if (typeof value === "object") {
    try {
      return JSON.stringify(value, (_key, inner) => (typeof inner === "bigint" ? inner.toString() : inner));
    } catch {
      return String(value);
    }
  }
  return String(value);
}
