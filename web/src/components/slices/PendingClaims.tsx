import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import type { PendingClaim } from "../../api/types";
import { useApi } from "../../api/useApi";
import { useAppAuth } from "../../auth/AppAuthProvider";
import { formatDate } from "../../lib/time";
import {
  authStatusKey,
  ownedAgentsKey,
  pendingClaimsKey,
  pendingClaimsQuery
} from "../../queries/auth";
import { slicesKeyPrefix } from "../../queries/slices";
import { Button } from "../ui/Button";
import { getErrorMessage } from "./SlicePageParts";

// PendingClaims lists agents that registered with one of the signed-in user's
// verified emails and lets them accept co-ownership. It renders nothing when
// there is nothing to claim or the lookup fails, so it never blocks the page.
export function PendingClaims({ showEmpty = false }: { showEmpty?: boolean }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const { authKind, isLoaded, isSignedIn } = useAppAuth();
  const enabled = Boolean(isLoaded && isSignedIn && authKind === "clerk");

  // One request answers both: the claims, and the verified emails they were
  // matched against, which explain an empty list. The top bar's pill shares it.
  const claimsQuery = useQuery({
    ...pendingClaimsQuery(api),
    enabled
  });

  const acceptMutation = useMutation({
    mutationFn: ({
      agentSubjectId,
      grantRequestedSlice
    }: {
      agentSubjectId: string;
      grantRequestedSlice?: boolean;
    }) =>
      grantRequestedSlice === undefined
        ? api.acceptClaim({ agentSubjectId })
        : api.acceptClaim({ agentSubjectId, grantRequestedSlice }),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: pendingClaimsKey }),
        queryClient.invalidateQueries({ queryKey: authStatusKey }),
        queryClient.invalidateQueries({ queryKey: ownedAgentsKey }),
        queryClient.invalidateQueries({ queryKey: slicesKeyPrefix })
      ]);
    }
  });

  const claims = claimsQuery.data?.claims ?? [];
  const accepted = acceptMutation.isSuccess ? acceptMutation.data.account : "";
  const grantedSlice = acceptMutation.isSuccess ? acceptMutation.data.grantedSlice : undefined;
  const grantedRole = acceptMutation.isSuccess ? acceptMutation.data.grantedRole : "";
  if (!enabled || (claims.length === 0 && !accepted)) {
    if (showEmpty && isLoaded && isSignedIn && authKind === "agent") {
      return (
        <div className="rounded-md border border-dashed border-slate-300 p-4 text-sm leading-6 text-slate-600 dark:border-zinc-700 dark:text-zinc-400">
          <p>
            You are signed in as an agent. Agent sessions cannot claim other agents — sign in with
            your human account to view or accept pending claims.
          </p>
        </div>
      );
    }
    if (showEmpty && enabled && claimsQuery.isSuccess) {
      return (
        <div className="rounded-md border border-dashed border-slate-300 p-4 text-sm leading-6 text-slate-600 dark:border-zinc-700 dark:text-zinc-400">
          <p>
            No agents are waiting to be claimed. An agent shows up here after it
            runs <code className="font-mono text-xs">gs onboard</code> or{" "}
            <code className="font-mono text-xs">gs auth register-agent</code>{" "}
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
          {grantedSlice?.account && grantedSlice?.slice ? (
            <>
              {" "}
              and granted {grantedRole || "writer"} access to{" "}
              <Link
                className="font-semibold underline underline-offset-2"
                params={{ account: grantedSlice.account, slice: grantedSlice.slice }}
                to="/slices/$account/$slice"
              >
                {grantedSlice.account}/{grantedSlice.slice}
              </Link>
            </>
          ) : null}
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
                acceptMutation.variables?.agentSubjectId === claim.agentSubjectId
              }
              key={claim.agentSubjectId}
              onAccept={(grantRequestedSlice) => {
                if (claim.agentSubjectId) {
                  acceptMutation.mutate({
                    agentSubjectId: claim.agentSubjectId,
                    grantRequestedSlice
                  });
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
  onAccept: (grantRequestedSlice?: boolean) => void;
}) {
  const hasRequestedSlice = Boolean(
    claim.requestedSlice?.account && claim.requestedSlice?.slice
  );
  const [grantSlice, setGrantSlice] = useState(true);
  const created = claim.createdAt ? new Date(claim.createdAt) : null;
  const requestedLabel = hasRequestedSlice
    ? `${claim.requestedSlice?.account}/${claim.requestedSlice?.slice}`
    : "";
  const requestedRole = claim.requestedRole || "writer";

  return (
    <li className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-slate-200 bg-white px-3 py-2.5 dark:border-zinc-800 dark:bg-zinc-900">
      <div className="min-w-0 space-y-1">
        <p className="truncate text-sm font-medium text-zinc-950 dark:text-zinc-50">
          {claim.agentDisplayName || claim.account}
        </p>
        <p className="truncate text-xs text-slate-500 dark:text-zinc-400">
          Account {claim.account}
          {created && !Number.isNaN(created.getTime())
            ? ` · registered ${formatDate(created)}`
            : ""}
        </p>
        {hasRequestedSlice ? (
          <label className="flex items-center gap-2 pt-0.5 text-xs text-slate-700 dark:text-zinc-300">
            <input
              checked={grantSlice}
              className="h-3.5 w-3.5 rounded border-slate-300 text-zinc-900 focus:ring-zinc-500 dark:border-zinc-600 dark:bg-zinc-800"
              onChange={(e) => setGrantSlice(e.target.checked)}
              type="checkbox"
            />
            <span>
              Also grant{" "}
              <span className="font-medium text-zinc-950 dark:text-zinc-100">
                {requestedRole}
              </span>{" "}
              access to{" "}
              <code className="font-mono text-[11px] font-medium text-zinc-950 dark:text-zinc-100">
                {requestedLabel}
              </code>
            </span>
          </label>
        ) : null}
      </div>
      <Button
        className="shrink-0"
        disabled={isAccepting}
        onClick={() => onAccept(hasRequestedSlice ? grantSlice : undefined)}
        variant="primary"
      >
        {isAccepting
          ? "Accepting..."
          : hasRequestedSlice && grantSlice
            ? "Accept & grant"
            : "Accept"}
      </Button>
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
