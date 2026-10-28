import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { useApi } from "../api/useApi";

/**
 * Tells a signed-in person, on every page, that agents registered with one
 * of their verified emails are waiting to be claimed. It shares the claims
 * page's query, so accepting there clears it.
 */
export function ClaimsNotice() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const claimsQuery = useQuery({
    enabled: Boolean(isLoaded && isSignedIn),
    queryKey: ["pendingClaims"],
    queryFn: () => api.listPendingClaims({}),
    retry: false,
    select: (response) => response.claims ?? [],
    staleTime: 60_000
  });
  const count = claimsQuery.data?.length ?? 0;
  if (!isSignedIn || count === 0) {
    return null;
  }
  const label = count === 1 ? "1 agent to claim" : `${count} agents to claim`;
  return (
    <Link
      aria-label={`${label}: review`}
      className="inline-flex items-center gap-1.5 rounded-full border border-sky-300 bg-sky-50 px-2.5 py-1 text-xs font-semibold text-sky-900 transition hover:border-sky-400 hover:bg-sky-100 dark:border-sky-900/70 dark:bg-sky-950/40 dark:text-sky-200 dark:hover:bg-sky-950/70"
      title="Agents registered with your email and are waiting for you to accept co-ownership"
      to="/claims"
    >
      <span aria-hidden className="size-1.5 rounded-full bg-sky-500" />
      <span className="sm:hidden">{count}</span>
      <span className="hidden sm:inline">{label}</span>
    </Link>
  );
}
