import { createContext, useContext, useEffect, useRef, useState } from "react";

import { cn } from "../../lib/cn";

// A collapsible tree for parsed JSON, YAML and TOML. Only expanded nodes render
// their children, and long arrays and objects show a page at a time, so a large
// document costs what is on screen.

const CHILDREN_PER_PAGE = 100;
const STRING_PREVIEW = 300;

interface TreeControl {
  nonce: number;
  open: boolean;
}

const TreeControlContext = createContext<TreeControl>({ nonce: 0, open: false });

export function JsonTree({ defaultDepth = 2, value }: { defaultDepth?: number; value: unknown }) {
  const [control, setControl] = useState<TreeControl>({ nonce: 0, open: false });

  return (
    <div>
      <div className="flex items-center gap-2 border-b border-slate-200 px-4 py-2 text-xs dark:border-zinc-800">
        <TreeButton onClick={() => setControl((c) => ({ nonce: c.nonce + 1, open: true }))}>Expand all</TreeButton>
        <TreeButton onClick={() => setControl((c) => ({ nonce: c.nonce + 1, open: false }))}>Collapse all</TreeButton>
      </div>
      <TreeControlContext.Provider value={control}>
        <div className="overflow-auto p-4 font-mono text-[13px] leading-6" role="tree">
          <Node defaultDepth={defaultDepth} depth={0} name={null} value={value} />
        </div>
      </TreeControlContext.Provider>
    </div>
  );
}

function TreeButton({ children, onClick }: { children: string; onClick(): void }) {
  return (
    <button
      className="rounded-md border border-slate-200 px-2 py-1 font-medium text-slate-700 transition hover:text-zinc-950 dark:border-zinc-800 dark:text-zinc-300 dark:hover:text-zinc-50"
      onClick={onClick}
      type="button"
    >
      {children}
    </button>
  );
}

type Entry = [key: string, value: unknown];

function entriesOf(value: unknown): Entry[] | null {
  if (Array.isArray(value)) {
    return value.map((item, index) => [String(index), item]);
  }
  if (value !== null && typeof value === "object" && !(value instanceof Date)) {
    return Object.entries(value as Record<string, unknown>);
  }
  return null;
}

function Node({
  defaultDepth,
  depth,
  name,
  value
}: {
  defaultDepth: number;
  depth: number;
  name: string | null;
  value: unknown;
}) {
  const entries = entriesOf(value);
  const control = useContext(TreeControlContext);
  // A node that appears after Expand all or Collapse all follows it.
  const [open, setOpen] = useState(control.nonce > 0 ? control.open : depth < defaultDepth);
  const [visible, setVisible] = useState(CHILDREN_PER_PAGE);
  const seenNonce = useRef(control.nonce);

  useEffect(() => {
    if (control.nonce !== seenNonce.current) {
      seenNonce.current = control.nonce;
      setOpen(control.open);
    }
  }, [control]);

  const label =
    name === null ? null : (
      <>
        <span className="text-sky-700 dark:text-sky-300">{Array.isArray(value) || /^\d+$/.test(name) ? name : JSON.stringify(name)}</span>
        <span className="text-slate-500 dark:text-zinc-500">: </span>
      </>
    );

  if (!entries) {
    return (
      <div className="whitespace-pre-wrap break-all pl-5" role="treeitem">
        {label}
        <Scalar value={value} />
      </div>
    );
  }

  const isArray = Array.isArray(value);
  const [openBracket, closeBracket] = isArray ? ["[", "]"] : ["{", "}"];
  const count = `${entries.length} ${entries.length === 1 ? (isArray ? "item" : "key") : isArray ? "items" : "keys"}`;

  return (
    <div role="treeitem" aria-expanded={open}>
      <button
        aria-label={`${open ? "Collapse" : "Expand"} ${name ?? "root"}`}
        className="flex w-full items-baseline gap-1 text-left hover:bg-slate-50 dark:hover:bg-zinc-950"
        onClick={() => setOpen((current) => !current)}
        type="button"
      >
        <span className="inline-block w-4 shrink-0 text-center text-slate-400" aria-hidden>
          {open ? "▾" : "▸"}
        </span>
        {label}
        <span className="text-slate-500 dark:text-zinc-400">
          {openBracket}
          {open ? "" : ` ${count} ${closeBracket}`}
        </span>
      </button>
      {open ? (
        <div className="ml-2 border-l border-slate-200 pl-3 dark:border-zinc-800" role="group">
          {entries.slice(0, visible).map(([key, child]) => (
            <Node defaultDepth={defaultDepth} depth={depth + 1} key={key} name={key} value={child} />
          ))}
          {visible < entries.length ? (
            <button
              className="py-1 pl-5 text-xs font-medium text-sky-700 hover:underline dark:text-sky-300"
              onClick={() => setVisible((current) => current + CHILDREN_PER_PAGE * 5)}
              type="button"
            >
              Show {Math.min(CHILDREN_PER_PAGE * 5, entries.length - visible)} more of {entries.length - visible}
            </button>
          ) : null}
          <div className="pl-3 text-slate-500 dark:text-zinc-400">{closeBracket}</div>
        </div>
      ) : null}
    </div>
  );
}

function Scalar({ value }: { value: unknown }) {
  if (value === null || value === undefined) {
    return <span className="text-slate-500 dark:text-zinc-500">null</span>;
  }
  if (typeof value === "string") {
    const long = value.length > STRING_PREVIEW;
    return (
      <span className="text-emerald-700 dark:text-emerald-300" title={long ? value : undefined}>
        {JSON.stringify(long ? `${value.slice(0, STRING_PREVIEW)}…` : value)}
      </span>
    );
  }
  if (typeof value === "number" || typeof value === "bigint") {
    return <span className="text-amber-700 dark:text-amber-300">{String(value)}</span>;
  }
  if (typeof value === "boolean") {
    return <span className={cn("font-semibold", value ? "text-violet-700 dark:text-violet-300" : "text-rose-700 dark:text-rose-300")}>{String(value)}</span>;
  }
  if (value instanceof Date) {
    return <span className="text-teal-700 dark:text-teal-300">{Number.isNaN(value.getTime()) ? "invalid date" : value.toISOString()}</span>;
  }
  return <span>{String(value)}</span>;
}
