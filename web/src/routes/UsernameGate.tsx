import type { ReactNode } from "react";

import { useSelection } from "../state/selection";
import { ChooseUsernamePage } from "./ChooseUsernamePage";

// Holds the app back until the signed-in person has a session and a username.
export function UsernameGate({ children }: { children: ReactNode }) {
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
        <section className="w-full max-w-md rounded-lg border border-rose-200 dark:border-rose-900/60 bg-white dark:bg-zinc-900 p-5 text-rose-900 dark:text-rose-200 shadow-sm shadow-slate-200/50 dark:shadow-black/50">
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
