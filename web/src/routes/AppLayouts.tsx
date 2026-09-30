import { Outlet } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { RequireAuth } from "../auth/RequireAuth";
import { AppShell } from "../components/AppShell";
import { SelectionProvider, useSelection } from "../state/selection";
import { ChooseUsernamePage } from "./ChooseUsernamePage";
import { HomePage } from "./HomePage";

export function AuthedAppLayout() {
  return (
    <RequireAuth>
      <SelectionProvider>
        <UsernameGate>
          <AppShell>
            <Outlet />
          </AppShell>
        </UsernameGate>
      </SelectionProvider>
    </RequireAuth>
  );
}

export function PublicAppLayout() {
  return (
    <SelectionProvider>
      <AppShell>
        <Outlet />
      </AppShell>
    </SelectionProvider>
  );
}

export function SignedInHome() {
  return (
    <RequireAuth>
      <SelectionProvider>
        <UsernameGate>
          <AppShell>
            <HomePage />
          </AppShell>
        </UsernameGate>
      </SelectionProvider>
    </RequireAuth>
  );
}

function UsernameGate({ children }: { children: ReactNode }) {
  const { error, isLoading, needsUsername } = useSelection();

  if (isLoading) {
    return (
      <main className="grid min-h-[100dvh] place-items-center bg-slate-50 dark:bg-zinc-950 p-6 text-sm text-slate-600 dark:text-zinc-400">
        Loading session...
      </main>
    );
  }

  if (error) {
    return (
      <main className="grid min-h-[100dvh] place-items-center bg-slate-50 dark:bg-zinc-950 p-6 text-sm text-slate-600 dark:text-zinc-400">
        <section className="w-full max-w-md rounded-lg border border-rose-200 bg-white p-5 text-rose-900 shadow-sm shadow-slate-200/50 dark:border-rose-900/60 dark:bg-zinc-900 dark:text-rose-200 dark:shadow-black/50">
          <p className="font-semibold">Could not load session</p>
          <p className="mt-2 leading-6">{error.message}</p>
        </section>
      </main>
    );
  }

  if (needsUsername) {
    return <ChooseUsernamePage />;
  }

  return <>{children}</>;
}
