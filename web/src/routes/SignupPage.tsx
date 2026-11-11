import { SignUp, useAuth } from "@clerk/tanstack-react-start";
import { Navigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { AuthFrame } from "../components/AuthFrame";
import { clerkUiInDocument, reloadForClerkUi } from "../lib/clerkUi";

/**
 * Account sign-up for people, inside the app (agents sign up with
 * gs auth register-agent). A new account then picks its username.
 */
export function SignupPage() {
  const { isLoaded, isSignedIn } = useAuth();

  // Reached from a page that did not load Clerk's UI: load this one from the
  // server, which includes it (lib/clerkUi).
  const [needsUi] = useState(() => !clerkUiInDocument());
  useEffect(() => {
    if (needsUi) {
      reloadForClerkUi();
    }
  }, [needsUi]);

  if (!isLoaded || needsUi) {
    return (
      <AuthFrame reserve="sign-up" title="Sign up">
        <p className="text-sm text-slate-600 dark:text-zinc-400">Loading session...</p>
      </AuthFrame>
    );
  }

  if (isSignedIn) {
    return <Navigate replace to="/" />;
  }

  return (
    <AuthFrame reserve="sign-up" title="Sign up" titleIsHeading={false}>
      <SignUp
        appearance={{
          elements: {
            // As on the sign-in page: Clerk's fixed-width card would be
            // clipped on narrow phones.
            rootBox: { width: "100%" },
            cardBox: { width: "100%" }
          }
        }}
        path="/signup"
        routing="path"
        signInUrl="/login"
      />
    </AuthFrame>
  );
}
