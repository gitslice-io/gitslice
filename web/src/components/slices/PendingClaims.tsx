import { useAuth } from "@clerk/tanstack-react-start";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import type { PendingClaim } from "../../api/types";
import { useApi } from "../../api/useApi";
import { getErrorMessage } from "./SlicePageParts";

// PendingClaims lists agents that registered with one of the signed-in user's
// verified emails and lets them accept co-ownership. It renders nothing when
// there is nothing to claim or the lookup fails, so it never blocks the page.
export function PendingClaims() {
  const api = useApi();
  const queryClient = useQueryClient();
  const { isLoaded, isSignedIn } = useAuth();
  const enabled = Boolean(isLoaded && isSignedIn);

  const claimsQuery = useQuery({
    enabled,
    queryKey: ["pendingClaims"],
    queryFn: async () => (await api.listPendingClaims({})).claims ?? [],
    retry: false
  });

  const acceptMutation = useMutation({
    mutationFn: (agentSubjectId: string) => api.acceptClaim({ agentSubjectId }),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["pendingClaims"] }),
        queryClient.invalidateQueries({ queryKey: ["authStatus"] }),
        queryClient.invalidateQueries({ queryKey: ["ownedAgents"] }),
        queryClient.invalidateQueries({ queryKey: ["slices"] })
      ]);
    }
  });

  const claims = claimsQuery.data ?? [];
  const accepted = acceptMutation.isSuccess ? acceptMutation.data.account : "";
  if (!enabled || (claims.length === 0 && !accepted)) {
    return null;
  }

  return (
    <section
      aria-labelledby="pending-claims-heading"
      className="rounded-lg border border-amber-200 bg-amber-50 p-4 dark:border-amber-900/60 dark:bg-amber-950/30"
    >
      <h2
        className="text-sm font-semibold text-zinc-950 dark:text-zinc-50"
        id="pending-claims-heading"
      >
        {claims.length === 1
          ? "An agent account is waiting for you"
          : claims.length > 1
            ? `${claims.length} agent accounts are waiting for you`
            : "Agent account claimed"}
      </h2>
      <p className="mt-1 text-sm text-slate-600 dark:text-zinc-400">
        These agents registered with your verified email. Accepting makes you a
        co-owner; the agent keeps its access.
      </p>

      {accepted ? (
        <p className="mt-3 text-sm text-emerald-800 dark:text-emerald-300" role="status">
          You now co-own{" "}
          <Link
            className="font-semibold underline underline-offset-2"
            params={{ account: accepted, slice: "home" }}
            to="/slices/$account/$slice"
          >
            {accepted}
          </Link>
          .
        </p>
      ) : null}

      {acceptMutation.isError ? (
        <p className="mt-3 text-sm text-rose-800 dark:text-rose-300" role="alert">
          {getErrorMessage(acceptMutation.error)}
        </p>
      ) : null}

      {claims.length > 0 ? (
        <ul className="mt-3 grid gap-2">
          {claims.map((claim) => (
            <PendingClaimRow
              claim={claim}
              isAccepting={
                acceptMutation.isPending &&
                acceptMutation.variables === claim.agentSubjectId
              }
              key={claim.agentSubjectId}
              onAccept={() => {
                if (claim.agentSubjectId) {
                  acceptMutation.mutate(claim.agentSubjectId);
                }
              }}
            />
          ))}
        </ul>
      ) : null}
    </section>
  );
}

function PendingClaimRow({
  claim,
  isAccepting,
  onAccept
}: {
  claim: PendingClaim;
  isAccepting: boolean;
  onAccept: () => void;
}) {
  const created = claim.createdAt ? new Date(claim.createdAt) : null;
  return (
    <li className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-slate-200 bg-white px-3 py-2 dark:border-zinc-800 dark:bg-zinc-900">
      <div className="min-w-0">
        <p className="truncate text-sm font-medium text-zinc-950 dark:text-zinc-50">
          {claim.agentDisplayName || claim.account}
        </p>
        <p className="truncate text-xs text-slate-500 dark:text-zinc-400">
          Account {claim.account}
          {created && !Number.isNaN(created.getTime())
            ? ` · registered ${created.toLocaleDateString()}`
            : ""}
        </p>
      </div>
      <button
        className="shrink-0 rounded-md bg-zinc-950 px-3 py-1.5 text-xs font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:opacity-60 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
        disabled={isAccepting}
        onClick={onAccept}
        type="button"
      >
        {isAccepting ? "Accepting..." : "Accept"}
      </button>
    </li>
  );
}
