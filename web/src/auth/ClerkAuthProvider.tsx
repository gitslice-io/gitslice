import { ClerkProvider, useAuth } from "@clerk/tanstack-react-start";
import { useEffect, useRef, type ReactNode } from "react";

import { aliasUser, identifyUser, resetUser } from "../analytics/posthog";

interface ClerkAuthProviderProps {
  children: ReactNode;
}

export function ClerkAuthProvider({ children }: ClerkAuthProviderProps) {
  const publishableKey = import.meta.env.VITE_CLERK_PUBLISHABLE_KEY;

  if (!publishableKey) {
    return (
      <main className="grid min-h-[100dvh] place-items-center bg-slate-50 dark:bg-zinc-950 p-6 text-zinc-900 dark:text-zinc-100">
        <section className="w-full max-w-md rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-6 shadow-sm">
          <p className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
            Configuration
          </p>
          <h1 className="mt-2 text-xl font-semibold">
            Missing Clerk publishable key
          </h1>
          <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            Set VITE_CLERK_PUBLISHABLE_KEY before running the web app.
          </p>
        </section>
      </main>
    );
  }

  return (
    <ClerkProvider
      afterSignOutUrl="/login"
      appearance={{
        variables: {
          colorBackground: "var(--clerk-color-background)",
          colorForeground: "var(--clerk-color-text)",
          colorInput: "var(--clerk-color-input-background)",
          colorInputForeground: "var(--clerk-color-text)",
          colorMutedForeground: "var(--clerk-color-text-secondary)",
          colorNeutral: "var(--clerk-color-neutral)"
        }
      }}
      publishableKey={publishableKey}
    >
      <ClerkAnalyticsIdentity />
      {children}
    </ClerkProvider>
  );
}

function ClerkAnalyticsIdentity() {
  const { isLoaded, isSignedIn, userId } = useAuth();
  const identifiedUserId = useRef<string | undefined>();

  useEffect(() => {
    if (!isLoaded) {
      return;
    }

    if (isSignedIn && userId) {
      if (identifiedUserId.current === userId) {
        return;
      }

      identifyUser(userId);
      aliasUser(userId);
      identifiedUserId.current = userId;
      return;
    }

    if (identifiedUserId.current) {
      resetUser();
      identifiedUserId.current = undefined;
    }
  }, [isLoaded, isSignedIn, userId]);

  return null;
}
