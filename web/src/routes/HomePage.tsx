import { Navigate } from "@tanstack/react-router";

import { useSelection } from "../state/selection";

// Home shows the signed-in user's files: it resolves their account from the
// session and sends them straight to the source browser rooted at their home
// directory. No account input — the account comes from the session.
export function HomePage() {
  const { account, isLoading } = useSelection();

  if (account) {
    return <Navigate replace params={{ account }} to="/source/$account" />;
  }

  return (
    <section className="mx-auto w-full max-w-3xl p-8 text-sm text-slate-600">
      {isLoading
        ? "Loading your workspace..."
        : "No account is associated with your session yet."}
    </section>
  );
}
