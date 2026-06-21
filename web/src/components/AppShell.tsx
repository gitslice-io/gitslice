import { Outlet } from "@tanstack/react-router";

import { TopBar } from "./TopBar";

// Pure layout. Account selection is persisted via localStorage and read from the
// `?account=` query param / path on load (see state/selection.tsx); AppShell must
// not write to history directly, or it desyncs the router and breaks navigation
// (clicks needing two presses to take effect).
export function AppShell() {
  return (
    <div className="min-h-[100dvh] bg-slate-50 text-zinc-900">
      <TopBar />
      <main className="px-2 py-6 sm:px-4 md:px-8 md:py-8">
        <Outlet />
      </main>
    </div>
  );
}
