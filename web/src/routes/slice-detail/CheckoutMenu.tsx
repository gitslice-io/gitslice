import { useRef, useEffect } from "react";
import { Link } from "@tanstack/react-router";

import type { ApiClient } from "../../api/useApi";
import { GLOBAL_REF_NAME } from "../../lib/globalRef";
import { CopyRow } from "./CopyRow";

interface CheckoutMenuProps {
  gitUrl: string;
  sliceRef: string;
}

export function CheckoutMenu({ gitUrl, sliceRef }: CheckoutMenuProps) {
  const gsCommand = `gs init ${sliceRef}`;
  const detailsRef = useRef<HTMLDetailsElement>(null);

  useEffect(() => {
    function close(event: Event) {
      const el = detailsRef.current;
      if (el?.open && !el.contains(event.target as Node)) {
        el.open = false;
      }
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape" && detailsRef.current?.open) {
        detailsRef.current.open = false;
      }
    }
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, []);

  return (
    <details className="group relative" ref={detailsRef}>
      <summary className="flex cursor-pointer list-none items-center gap-1 rounded-md px-2 py-1.5 text-xs font-medium text-slate-500 dark:text-zinc-400 transition hover:bg-slate-100 dark:hover:bg-zinc-800 hover:text-slate-800 dark:hover:text-zinc-200">
        Checkout
        <span aria-hidden="true" className="text-[0.65rem] text-slate-400 dark:text-zinc-500 transition group-open:rotate-180">
          ▾
        </span>
      </summary>
      <div className="fixed left-4 right-4 z-20 mt-2 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-3 shadow-lg shadow-slate-900/10 sm:absolute sm:left-auto sm:right-0 sm:w-[min(26rem,calc(100vw-2rem))]">
        <div className="flex items-center gap-2">
          <span className="text-xs font-semibold uppercase tracking-normal text-slate-600 dark:text-zinc-400">
            gs CLI
          </span>
          <span className="rounded bg-emerald-50 dark:bg-emerald-950/30 px-1.5 py-0.5 text-[0.65rem] font-semibold uppercase tracking-normal text-emerald-700 dark:text-emerald-300">
            recommended
          </span>
        </div>
        <CopyRow value={gsCommand} />
        <p className="mt-1.5 text-xs leading-5 text-slate-500 dark:text-zinc-400">
          Creates a workspace for this slice. Sign in first with{" "}
          <code className="rounded bg-slate-100 dark:bg-zinc-800 px-1 py-0.5 font-mono text-[0.7rem] text-slate-700 dark:text-zinc-300">
            gs auth login
          </code>
          . New to gs? See{" "}
          <Link
            className="text-slate-700 dark:text-zinc-300 underline underline-offset-2 hover:text-zinc-950 dark:hover:text-zinc-50"
            params={{ section: "cli" }}
            to="/doc/$section"
          >
            CLI docs
          </Link>
          .
        </p>

        <div className="mt-3 border-t border-slate-100 dark:border-zinc-800 pt-3">
          <span className="text-xs font-semibold uppercase tracking-normal text-slate-600 dark:text-zinc-400">
            Git
          </span>
          <CopyRow value={`git clone ${gitUrl}`} />
        </div>
      </div>
    </details>
  );
}