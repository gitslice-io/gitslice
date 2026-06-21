import { useAuth } from "@clerk/tanstack-react-start";
import { Navigate } from "@tanstack/react-router";
import type { ReactNode } from "react";

interface RequireAuthProps {
  children: ReactNode;
}

export function RequireAuth({ children }: RequireAuthProps) {
  const { isLoaded, isSignedIn } = useAuth();

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
