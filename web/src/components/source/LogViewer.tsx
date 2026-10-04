import { useDeferredValue, useMemo, useState } from "react";

import { cn } from "../../lib/cn";
import { stripAnsi } from "../../lib/sanitize";

// A log preview: each line coloured by level, with a text filter and level
// toggles. Long logs show the first matching lines and reveal more on request.

const LINES_PER_PAGE = 2000;

export type LogLevel = "error" | "warn" | "info" | "debug";

const LEVEL_PATTERN =
  /(?:\blevel[=:"'\s]+)?\b(FATAL|CRITICAL|PANIC|SEVERE|ERROR|ERR|WARNING|WARN|NOTICE|INFO|DEBUG|TRACE|VERBOSE)\b/i;

const LEVELS: Record<string, LogLevel> = {
  CRITICAL: "error",
  DEBUG: "debug",
  ERR: "error",
  ERROR: "error",
  FATAL: "error",
  INFO: "info",
  NOTICE: "info",
  PANIC: "error",
  SEVERE: "error",
  TRACE: "debug",
  VERBOSE: "debug",
  WARN: "warn",
  WARNING: "warn"
};

// logLevel finds a line's level in its first 120 characters, where timestamps
// and level markers sit.
export function logLevel(line: string): LogLevel | null {
  const match = LEVEL_PATTERN.exec(line.slice(0, 120));
  return match ? LEVELS[match[1].toUpperCase()] : null;
}

const LEVEL_CLASSES: Record<LogLevel, string> = {
  debug: "text-slate-500 dark:text-zinc-500",
  error: "bg-rose-50 text-rose-800 dark:bg-rose-950/30 dark:text-rose-200",
  info: "text-zinc-900 dark:text-zinc-100",
  warn: "bg-amber-50 text-amber-800 dark:bg-amber-950/30 dark:text-amber-200"
};

const LEVEL_ORDER: LogLevel[] = ["error", "warn", "info", "debug"];

export function LogViewer({ source }: { source: string }) {
  const lines = useMemo(() => {
    const text = stripAnsi(source).replace(/\n$/, "");
    return text === "" ? [] : text.split(/\r\n|\r|\n/).map((text, index) => ({ index: index + 1, level: logLevel(text), text }));
  }, [source]);
  const [filter, setFilter] = useState("");
  const [hidden, setHidden] = useState<Set<LogLevel>>(new Set());
  const [wrap, setWrap] = useState(false);
  const [visible, setVisible] = useState(LINES_PER_PAGE);
  const deferredFilter = useDeferredValue(filter);

  const counts = useMemo(() => {
    const result: Record<LogLevel, number> = { debug: 0, error: 0, info: 0, warn: 0 };
    for (const line of lines) {
      if (line.level) {
        result[line.level]++;
      }
    }
    return result;
  }, [lines]);

  const needle = deferredFilter.trim().toLowerCase();
  const matching = useMemo(
    () => lines.filter((line) => (!line.level || !hidden.has(line.level)) && (needle === "" || line.text.toLowerCase().includes(needle))),
    [lines, hidden, needle]
  );

  if (lines.length === 0) {
    return <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">This file is empty.</p>;
  }

  function toggle(level: LogLevel) {
    setHidden((current) => {
      const next = new Set(current);
      if (next.has(level)) {
        next.delete(level);
      } else {
        next.add(level);
      }
      return next;
    });
  }

  const shown = matching.slice(0, visible);
  const width = String(lines.length).length;

  return (
    <div>
      <div className="flex flex-wrap items-center gap-3 border-b border-slate-200 px-4 py-2 text-xs dark:border-zinc-800">
        <input
          aria-label="Filter log lines"
          className="min-w-40 flex-1 rounded-md border border-slate-200 bg-white px-2 py-1 text-zinc-900 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-100"
          onChange={(event) => {
            setFilter(event.target.value);
            setVisible(LINES_PER_PAGE);
          }}
          placeholder="Filter lines"
          type="search"
          value={filter}
        />
        {LEVEL_ORDER.filter((level) => counts[level] > 0).map((level) => (
          <button
            aria-pressed={!hidden.has(level)}
            className={cn(
              "rounded-md border px-2 py-1 font-medium capitalize",
              hidden.has(level)
                ? "border-slate-200 text-slate-400 line-through dark:border-zinc-800 dark:text-zinc-600"
                : "border-slate-300 text-slate-700 dark:border-zinc-700 dark:text-zinc-300"
            )}
            key={level}
            onClick={() => toggle(level)}
            type="button"
          >
            {level} {counts[level].toLocaleString()}
          </button>
        ))}
        <label className="inline-flex items-center gap-2 text-slate-600 dark:text-zinc-400">
          <input checked={wrap} onChange={(event) => setWrap(event.target.checked)} type="checkbox" />
          Wrap
        </label>
        <span className="text-slate-500 dark:text-zinc-400">
          {matching.length === lines.length
            ? `${lines.length.toLocaleString()} lines`
            : `${matching.length.toLocaleString()} of ${lines.length.toLocaleString()} lines`}
        </span>
      </div>
      <div className="max-h-[70dvh] overflow-auto py-2 font-mono text-[13px] leading-5">
        {shown.map((line) => (
          <div className={cn("flex gap-3 px-4", line.level ? LEVEL_CLASSES[line.level] : "text-zinc-900 dark:text-zinc-100")} key={line.index}>
            <span aria-hidden className="select-none text-right text-slate-400 dark:text-zinc-600" style={{ minWidth: `${width}ch` }}>
              {line.index}
            </span>
            <span className={wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre"}>{line.text}</span>
          </div>
        ))}
        {matching.length === 0 ? <p className="px-4 py-2 text-slate-500 dark:text-zinc-400">No line matches.</p> : null}
      </div>
      {shown.length < matching.length ? (
        <div className="flex items-center justify-between gap-3 border-t border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
          <span>
            Showing {shown.length.toLocaleString()} of {matching.length.toLocaleString()} lines
          </span>
          <button
            className="rounded-md border border-slate-200 px-2.5 py-1 font-medium text-slate-700 dark:border-zinc-800 dark:text-zinc-300"
            onClick={() => setVisible((current) => current + LINES_PER_PAGE * 5)}
            type="button"
          >
            Show more
          </button>
        </div>
      ) : null}
    </div>
  );
}
