import { useAuth } from "@clerk/clerk-react";
import { Navigate } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";

import { CLI_LOGIN_SEARCH_STORAGE_KEY } from "../auth/cliLogin";
import { AuthFrame } from "../components/AuthFrame";

export function CliLoginPage() {
  const { getToken, isLoaded, isSignedIn } = useAuth();
  const [error, setError] = useState<string | null>(null);
  const params = useMemo(() => new URLSearchParams(window.location.search), []);
  const callbackUrl = params.get("callback_url");
  const state = params.get("state") ?? "";

  useEffect(() => {
    if (!isLoaded || !isSignedIn) {
      return;
    }

    let cancelled = false;

    async function authorizeCli() {
      if (!callbackUrl) {
        setError("Missing callback_url query parameter.");
        return;
      }

      try {
        const token = await getToken();
        if (!token) {
          throw new Error("Clerk did not return a session token.");
        }
        if (cancelled) {
          return;
        }
        const redirectUrl = `${callbackUrl}?state=${encodeURIComponent(
          state
        )}&token=${encodeURIComponent(token)}`;
        window.location.replace(redirectUrl);
      } catch (caught) {
        if (!cancelled) {
          setError(caught instanceof Error ? caught.message : String(caught));
        }
      }
    }

    void authorizeCli();

    return () => {
      cancelled = true;
    };
  }, [callbackUrl, getToken, isLoaded, isSignedIn, state]);

  if (!isLoaded) {
    return (
      <AuthFrame title="Authorizing CLI">
        <p className="text-sm text-slate-600">Loading session...</p>
      </AuthFrame>
    );
  }

  if (!isSignedIn) {
    sessionStorage.setItem(CLI_LOGIN_SEARCH_STORAGE_KEY, window.location.search);
    return <Navigate replace to="/login" />;
  }

  return (
    <AuthFrame title="Authorizing CLI">
      {error ? (
        <p className="text-sm text-red-700">{error}</p>
      ) : (
        <p className="text-sm text-slate-600">Authorizing CLI...</p>
      )}
    </AuthFrame>
  );
}
