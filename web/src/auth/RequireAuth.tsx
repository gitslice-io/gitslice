import { useAuth } from "@clerk/clerk-react";
import { Navigate } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { hasMintedToken } from "./token";

interface RequireAuthProps {
  children: ReactNode;
}

export function RequireAuth({ children }: RequireAuthProps) {
  const { isLoaded, isSignedIn } = useAuth();

  // A minted token is a complete session on its own; don't wait on Clerk.
  if (hasMintedToken()) {
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
