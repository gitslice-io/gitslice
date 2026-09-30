import { SignIn, useAuth } from "@clerk/tanstack-react-start";
import { Navigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { AuthFrame } from "../components/AuthFrame";
import { CLI_LOGIN_SEARCH_STORAGE_KEY } from "../auth/cliLogin";
import { clearReturnTo, peekReturnTo } from "../auth/returnTo";

export function LoginPage() {
  const { isLoaded, isSignedIn } = useAuth();
  // Read once: the page the user was on before being sent to sign in.
  const [returnTo] = useState(peekReturnTo);
  const pendingCliLoginSearch =
    typeof window === "undefined"
      ? null
      : sessionStorage.getItem(CLI_LOGIN_SEARCH_STORAGE_KEY);

  useEffect(() => {
    if (!isLoaded || !isSignedIn || !pendingCliLoginSearch) {
      return;
    }

    sessionStorage.removeItem(CLI_LOGIN_SEARCH_STORAGE_KEY);
    window.location.replace(`/cli-login${pendingCliLoginSearch}`);
  }, [isLoaded, isSignedIn, pendingCliLoginSearch]);

  useEffect(() => {
    if (!isLoaded || !isSignedIn || pendingCliLoginSearch || !returnTo) {
      return;
    }
    clearReturnTo();
    window.location.replace(returnTo);
  }, [isLoaded, isSignedIn, pendingCliLoginSearch, returnTo]);

  if (!isLoaded) {
    return (
      <AuthFrame title="Sign in">
        <p className="text-sm text-slate-600">Loading session...</p>
      </AuthFrame>
    );
  }

  if (isSignedIn && pendingCliLoginSearch) {
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
    <AuthFrame title="Sign in">
      <SignIn
        appearance={{
          elements: {
            // Clerk's card is a fixed 25rem wide by default, which gets
            // clipped by AuthFrame's overflow-hidden card on narrow phones.
            rootBox: { width: "100%" },
            cardBox: {
              backgroundColor: "transparent",
              border: "none",
              boxShadow: "none",
              width: "100%"
            },
            card: {
              backgroundColor: "transparent",
              border: "none",
              boxShadow: "none",
              width: "100%"
            },
            header: { display: "none" }
          }
        }}
        path="/login"
        routing="path"
      />
    </AuthFrame>
  );
}
