import { SignIn } from "@clerk/tanstack-react-start";
import { Navigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { useAppAuth } from "../auth/AppAuthProvider";
import { CLI_LOGIN_SEARCH_STORAGE_KEY } from "../auth/cliLogin";
import { clearReturnTo, peekReturnTo } from "../auth/returnTo";
import { AgentSignInForm } from "../components/AgentSignInForm";
import { AuthFrame } from "../components/AuthFrame";
import { clerkUiInDocument, reloadForClerkUi } from "../lib/clerkUi";
import { cn } from "../lib/cn";

type LoginMode = "human" | "agent";

function initialLoginMode(): LoginMode {
  if (typeof window === "undefined") {
    return "human";
  }
  const mode = new URLSearchParams(window.location.search).get("mode");
  return mode === "agent" ? "agent" : "human";
}

export function LoginPage() {
  const { authKind, isLoaded, isSignedIn } = useAppAuth();
  const [mode, setMode] = useState<LoginMode>(initialLoginMode);
  // Read once: the page the user was on before being sent to sign in.
  const [returnTo] = useState(peekReturnTo);
  const pendingCliLoginSearch =
    typeof window === "undefined"
      ? null
      : sessionStorage.getItem(CLI_LOGIN_SEARCH_STORAGE_KEY);

  useEffect(() => {
    if (!isLoaded || !isSignedIn || authKind !== "clerk" || !pendingCliLoginSearch) {
      return;
    }

    sessionStorage.removeItem(CLI_LOGIN_SEARCH_STORAGE_KEY);
    window.location.replace(`/cli-login${pendingCliLoginSearch}`);
  }, [authKind, isLoaded, isSignedIn, pendingCliLoginSearch]);

  useEffect(() => {
    if (!isLoaded || !isSignedIn || (authKind === "clerk" && pendingCliLoginSearch) || !returnTo) {
      return;
    }
    clearReturnTo();
    window.location.replace(returnTo);
  }, [authKind, isLoaded, isSignedIn, pendingCliLoginSearch, returnTo]);

  // Reached from a page that did not load Clerk's UI: load this one from the
  // server, which includes it (lib/clerkUi). Agent sign-in does not need it.
  const [needsUi] = useState(() => !clerkUiInDocument());
  useEffect(() => {
    if (mode === "human" && needsUi) {
      reloadForClerkUi();
    }
  }, [mode, needsUi]);

  if (mode === "human" && (!isLoaded || needsUi)) {
    return (
      <AuthFrame reserve="sign-in" title="Sign in">
        <p className="text-sm text-slate-600 dark:text-zinc-400">Loading session...</p>
      </AuthFrame>
    );
  }

  if (isSignedIn && authKind === "clerk" && pendingCliLoginSearch) {
    return (
      <AuthFrame title="Authorizing CLI">
        <p className="text-sm text-slate-600">Returning to CLI login...</p>
      </AuthFrame>
    );
  }

  if (isSignedIn && returnTo) {
    return (
      <AuthFrame title="Signed in">
        <p className="text-sm text-slate-600">Returning to your page...</p>
      </AuthFrame>
    );
  }

  if (isSignedIn) {
    return <Navigate replace to="/" />;
  }

  return (
    <AuthFrame
      reserve={mode === "human" ? "sign-in" : undefined}
      title="Sign in"
      titleIsHeading={mode === "agent"}
    >
      <div
        aria-label="Sign-in method"
        className="mb-4 grid grid-cols-2 rounded-md border border-slate-200 bg-slate-100 p-1 text-xs font-semibold dark:border-zinc-800 dark:bg-zinc-950"
        role="tablist"
      >
        <button
          aria-selected={mode === "human"}
          className={cn(
            "rounded px-3 py-1.5 transition",
            mode === "human"
              ? "bg-white text-zinc-950 shadow-sm dark:bg-zinc-800 dark:text-zinc-50"
              : "text-slate-600 hover:text-zinc-950 dark:text-zinc-400 dark:hover:text-zinc-100"
          )}
          onClick={() => setMode("human")}
          role="tab"
          type="button"
        >
          Human
        </button>
        <button
          aria-selected={mode === "agent"}
          className={cn(
            "rounded px-3 py-1.5 transition",
            mode === "agent"
              ? "bg-white text-zinc-950 shadow-sm dark:bg-zinc-800 dark:text-zinc-50"
              : "text-slate-600 hover:text-zinc-950 dark:text-zinc-400 dark:hover:text-zinc-100"
          )}
          onClick={() => setMode("agent")}
          role="tab"
          type="button"
        >
          Agent API key
        </button>
      </div>

      {mode === "agent" ? (
        <AgentSignInForm
          onSignedIn={() => {
            if (returnTo) {
              clearReturnTo();
              window.location.replace(returnTo);
            }
          }}
        />
      ) : (
        <SignIn
          appearance={{
            elements: {
              // Clerk's card is a fixed 25rem wide by default, which gets
              // clipped by AuthFrame's overflow-hidden card on narrow phones.
              rootBox: { width: "100%" },
              cardBox: { width: "100%" }
            }
          }}
          path="/login"
          routing="path"
          signUpUrl="/signup"
        />
      )}
    </AuthFrame>
  );
}
