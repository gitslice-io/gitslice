import { useAuth } from "@clerk/clerk-react";
import { Navigate } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { devAuthEnabled, getDevToken } from "./devAuth";

interface RequireAuthProps {
  children: ReactNode;
}

export function RequireAuth({ children }: RequireAuthProps) {
  const { isLoaded, isSignedIn } = useAuth();

  // Dev/testing bypass (compiled out unless VITE_ENABLE_DEV_AUTH): a present dev
  // token counts as authenticated so authed views can be driven headlessly.
  if (devAuthEnabled && getDevToken()) {
    return <>{children}</>;
  }

  if (!isLoaded) {
    return (
      <main className="grid min-h-[100dvh] place-items-center bg-slate-50 p-6 text-sm text-slate-600">
        Loading session...
      </main>
    );
  }

  if (!isSignedIn) {
    return <Navigate replace to="/login" />;
  }

  return <>{children}</>;
}
