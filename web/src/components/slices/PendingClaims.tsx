import { useAuth } from "@clerk/tanstack-react-start";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import type { PendingClaim } from "../../api/types";
import { useApi } from "../../api/useApi";
import { getErrorMessage } from "./SlicePageParts";
import { formatDate } from "../../lib/time";

// PendingClaims lists agents that registered with one of the signed-in user's
// verified emails and lets them accept co-ownership. It renders nothing when
// there is nothing to claim or the lookup fails, so it never blocks the page.
export function PendingClaims({ showEmpty = false }: { showEmpty?: boolean }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const { isLoaded, isSignedIn } = useAuth();
  const enabled = Boolean(isLoaded && isSignedIn);

  // One request answers both: the claims, and the verified emails they were
  // matched against, which explain an empty list. The top bar's pill shares it.
  const claimsQuery = useQuery({
    enabled,
    queryKey: ["pendingClaims"],
    queryFn: () => api.listPendingClaims({}),
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

  const claims = claimsQuery.data?.claims ?? [];
  const accepted = acceptMutation.isSuccess ? acceptMutation.data.account : "";
  if (!enabled || (claims.length === 0 && !accepted)) {
    if (showEmpty && enabled && claimsQuery.isSuccess) {
      return (
        <div className="rounded-md border border-dashed border-slate-300 p-4 text-sm leading-6 text-slate-600 dark:border-zinc-700 dark:text-zinc-400">
          <p>
            No agents are waiting to be claimed. An agent shows up here after it
            runs <code className="font-mono text-xs">gs auth register-agent</code>{" "}
            with one of your verified emails.
          </p>
          <SearchedEmails emails={claimsQuery.data?.verifiedEmails ?? []} />
        </div>
      );
    }
    if (showEmpty && claimsQuery.isError) {
      return (
        <div className="rounded-md border border-rose-200 bg-rose-50 p-4 text-sm leading-6 text-rose-900 dark:border-rose-900/60 dark:bg-rose-950/30 dark:text-rose-200" role="alert">
          Could not check for agents to claim: {getErrorMessage(claimsQuery.error)}
        </div>
      );
    }
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
            ? ` · registered ${formatDate(created)}`
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

// SearchedEmails says which addresses an empty claims list was matched
// against. An agent registered with any other address cannot be claimed by
// this account, and without this the list is just silently empty.
function SearchedEmails({ emails }: { emails: string[] }) {
  if (emails.length === 0) {
    return (
      <p className="mt-2" data-testid="searched-emails">
        Your account has no verified email. Add and verify the email your agent
        was registered with in your profile (Manage account), then reload.
      </p>
    );
  }
  return (
    <div className="mt-2" data-testid="searched-emails">
      <p>Looked for agents registered to:</p>
      <ul className="mt-1 list-inside list-disc font-mono text-xs text-zinc-900 dark:text-zinc-100">
        {emails.map((email) => (
          <li key={email}>{email}</li>
        ))}
      </ul>
      <p className="mt-2">
        If your agent was registered with another address, add and verify it in
        your profile (Manage account), then reload. A claim matches the exact
        email the agent was given.
      </p>
    </div>
  );
}
