import { Outlet } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { AppFooter } from "./AppFooter";
import { TopBar } from "./TopBar";

// Pure layout. Account selection is persisted via localStorage and read from the
// `?account=` query param / path on load (see state/selection.tsx); AppShell must
// not write to history directly, or it desyncs the router and breaks navigation
// (clicks needing two presses to take effect).
export function AppShell({ banner, children }: { banner?: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex min-h-[100dvh] flex-col bg-slate-50 text-zinc-900 transition-colors duration-200 dark:bg-zinc-950 dark:text-zinc-100">
      <a
        className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-zinc-950 focus:px-3.5 py-2 focus:text-sm focus:font-semibold focus:text-white focus:outline-none focus:ring-2 focus:ring-sky-500 dark:focus:bg-white dark:focus:text-zinc-950"
        href="#app-main"
      >
        Skip to main content
      </a>
      <TopBar />
      {banner}
      <main
        className="flex-1 px-2 py-6 outline-none sm:px-4 md:px-8 md:py-8"
        id="app-main"
        tabIndex={-1}
      >
        {children ?? <Outlet />}
      </main>
      <AppFooter />
    </div>
  );
}
