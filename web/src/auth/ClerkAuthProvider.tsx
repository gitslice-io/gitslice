import { ClerkProvider } from "@clerk/clerk-react";
import type { ReactNode } from "react";

interface ClerkAuthProviderProps {
  children: ReactNode;
}

export function ClerkAuthProvider({ children }: ClerkAuthProviderProps) {
  const publishableKey = import.meta.env.VITE_CLERK_PUBLISHABLE_KEY;

  if (!publishableKey) {
    return (
      <main className="grid min-h-[100dvh] place-items-center bg-slate-50 p-6 text-zinc-900">
        <section className="w-full max-w-md rounded-lg border border-slate-200 bg-white p-6 shadow-sm">
          <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
            Configuration
          </p>
          <h1 className="mt-2 text-xl font-semibold">
            Missing Clerk publishable key
          </h1>
          <p className="mt-3 text-sm leading-6 text-slate-600">
            Set VITE_CLERK_PUBLISHABLE_KEY before running the web app.
          </p>
        </section>
      </main>
    );
  }

  return <ClerkProvider publishableKey={publishableKey}>{children}</ClerkProvider>;
}
