import { SignUp, useAuth } from "@clerk/tanstack-react-start";
import { Navigate } from "@tanstack/react-router";

import { AuthFrame } from "../components/AuthFrame";

/**
 * Account sign-up for people, inside the app (agents sign up with
 * gs auth register-agent). A new account then picks its username.
 */
export function SignupPage() {
  const { isLoaded, isSignedIn } = useAuth();

  if (!isLoaded) {
    return (
      <AuthFrame title="Sign up">
        <p className="text-sm text-slate-600">Loading session...</p>
      </AuthFrame>
    );
  }

  if (isSignedIn) {
    return <Navigate replace to="/" />;
  }

  return (
    <AuthFrame title="Sign up" titleIsHeading={false}>
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
