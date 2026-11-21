import { useMutation } from "@tanstack/react-query";
import { Navigate } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";

import { useApi } from "../api/useApi";
import { useAppAuth } from "../auth/AppAuthProvider";
import { CLI_LOGIN_SEARCH_STORAGE_KEY } from "../auth/cliLogin";
import { AuthFrame } from "../components/AuthFrame";
import { Button } from "../components/ui/Button";

export function CliLoginPage() {
  const { authKind, getToken, isLoaded, isSignedIn, signOut } = useAppAuth();
  const api = useApi();
  const [error, setError] = useState<string | null>(null);
  const [completeSucceeded, setCompleteSucceeded] = useState(false);
  const completedCodeRef = useRef<string | null>(null);
  const startedCodeRef = useRef<string | null>(null);
  const callbackStartedRef = useRef(false);
  const params = useMemo(() => new URLSearchParams(window.location.search), []);
  const code = params.get("code") ?? "";
  const callbackUrl = params.get("callback_url");
  const state = params.get("state") ?? "";
  const isHumanSession = isLoaded && isSignedIn && authKind === "clerk";

  const { mutate: completeCliLogin } = useMutation({
    mutationFn: (requestedCode: string) =>
      api.completeCliLogin({ code: requestedCode }),
    onMutate: () => {
      if (!completedCodeRef.current) {
        setError(null);
      }
    },
    onError: (caught) => {
      if (!completedCodeRef.current) {
        setError(caught instanceof Error ? caught.message : String(caught));
      }
    },
    onSuccess: (_response, requestedCode) => {
      completedCodeRef.current = requestedCode;
      setCompleteSucceeded(true);
      setError(null);
    }
  });

  useEffect(() => {
    if (!isHumanSession || !code) {
      return;
    }

    if (startedCodeRef.current === code) {
      return;
    }

    startedCodeRef.current = code;
    completeCliLogin(code);
  }, [code, completeCliLogin, isHumanSession]);

  useEffect(() => {
    if (!isHumanSession || code || !callbackUrl) {
      return;
    }

    if (callbackStartedRef.current) {
      return;
    }

    callbackStartedRef.current = true;
    let cancelled = false;

    async function authorizeCli() {
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
  }, [callbackUrl, code, getToken, isHumanSession, state]);

  if (!isLoaded) {
    return (
      <AuthFrame title="Authorizing CLI">
        <p className="text-sm text-slate-600 dark:text-zinc-400">Loading session...</p>
      </AuthFrame>
    );
  }

  if (isSignedIn && authKind === "agent") {
    return (
      <AuthFrame title="Human account required">
        <div className="space-y-4">
          <p className="text-sm leading-6 text-slate-600 dark:text-zinc-400">
            This browser is currently signed in as an agent. Browser CLI login approves human
            sessions — sign out of the agent session to continue with your human account.
          </p>
          <Button
            className="w-full"
            onClick={() => {
              sessionStorage.setItem(CLI_LOGIN_SEARCH_STORAGE_KEY, window.location.search);
              void signOut().then(() => {
                window.location.replace("/login");
              });
            }}
            type="button"
          >
            Sign out of agent session
          </Button>
        </div>
      </AuthFrame>
    );
  }

  if (!isSignedIn) {
    sessionStorage.setItem(CLI_LOGIN_SEARCH_STORAGE_KEY, window.location.search);
    return <Navigate replace to="/login" />;
  }

  const title = completeSucceeded ? "You're signed in" : "Authorizing CLI";

  return (
    <AuthFrame title={title}>
      {completeSucceeded ? (
        <p className="text-sm text-slate-600 dark:text-zinc-400">
          Authorization complete — return to your terminal.
        </p>
      ) : error ? (
        <p className="text-sm text-red-700">{error}</p>
      ) : !code && !callbackUrl ? (
        <p className="text-sm text-red-700">Missing code query parameter.</p>
      ) : (
        <p className="text-sm text-slate-600 dark:text-zinc-400">Authorizing CLI...</p>
      )}
    </AuthFrame>
  );
}
