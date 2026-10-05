import { useAuth } from "@clerk/tanstack-react-start";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { useApi } from "../../api/useApi";
import { useSelection } from "../../state/selection";
import { getErrorMessage } from "./SlicePageParts";

export const MY_INVITATIONS_KEY = ["myInvitations"] as const;

// useMyInvitations is the signed-in user's pending invitations to
// organizations. Failures count as none, so it never blocks a page.
export function useMyInvitations() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  return useQuery({
    enabled: Boolean(isLoaded && isSignedIn),
    queryKey: MY_INVITATIONS_KEY,
    queryFn: async () => (await api.listMyInvitations({})).invitations ?? [],
    retry: false
  });
}

// PendingInvitations lists invitations to organizations, to accept or decline.
// Accepting makes the organization one of your accounts, and switches to it.
export function PendingInvitations() {
  const api = useApi();
  const queryClient = useQueryClient();
  const { setActiveAccount } = useSelection();
  const invitations = useMyInvitations().data ?? [];

  const respond = useMutation({
    mutationFn: (input: { account: string; accept: boolean }) => api.respondToInvitation(input),
    onSuccess: async (_result, input) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: MY_INVITATIONS_KEY }),
        queryClient.invalidateQueries({ queryKey: ["authStatus"] })
      ]);
      if (input.accept) {
        setActiveAccount(input.account);
      }
    }
  });

  const joined = respond.isSuccess && respond.variables?.accept ? respond.variables.account : "";
  if (invitations.length === 0 && !joined) {
    return null;
  }

  return (
    <section
      aria-labelledby="pending-invitations-heading"
      className="rounded-lg border border-sky-200 bg-sky-50 p-4 dark:border-sky-900/60 dark:bg-sky-950/30"
    >
      <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50" id="pending-invitations-heading">
        {invitations.length === 1
          ? "You are invited to an organization"
          : invitations.length > 1
            ? `You are invited to ${invitations.length} organizations`
            : "Invitation accepted"}
      </h2>
      {joined ? (
        <p className="mt-2 text-sm text-emerald-800 dark:text-emerald-300" role="status">
          You joined{" "}
          <Link className="font-semibold underline underline-offset-2" params={{ account: joined }} to="/accounts/$account">
            {joined}
          </Link>{" "}
          and are now working in it.
        </p>
      ) : null}
      {respond.isError ? (
        <p className="mt-2 text-sm text-rose-800 dark:text-rose-300" role="alert">
          {getErrorMessage(respond.error)}
        </p>
      ) : null}
      {invitations.length > 0 ? (
        <ul className="mt-3 grid gap-2">
          {invitations.map((invitation) => {
            const account = invitation.account ?? "";
            const busy = respond.isPending && respond.variables?.account === account;
            return (
              <li
                className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-slate-200 bg-white px-3 py-2 dark:border-zinc-800 dark:bg-zinc-900"
                key={account}
              >
                <div className="min-w-0">
                  <Link
                    className="truncate text-sm font-medium text-zinc-950 hover:underline dark:text-zinc-50"
                    params={{ account }}
                    to="/accounts/$account"
                  >
                    {account}
                  </Link>
                  <p className="truncate text-xs text-slate-500 dark:text-zinc-400">
                    As {invitation.role}
                    {invitation.invitedBy ? `, invited by ${invitation.invitedBy}` : ""}
                  </p>
                </div>
                <div className="flex gap-2">
                  <button
                    className="rounded-md bg-zinc-950 px-3 py-1.5 text-xs font-semibold text-white transition hover:bg-zinc-800 disabled:opacity-60 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
                    disabled={busy}
                    onClick={() => respond.mutate({ account, accept: true })}
                    type="button"
                  >
                    Accept
                  </button>
                  <button
                    className="rounded-md border border-slate-300 px-3 py-1.5 text-xs font-medium text-slate-700 transition hover:bg-slate-50 disabled:opacity-60 dark:border-zinc-700 dark:text-zinc-300 dark:hover:bg-zinc-950"
                    disabled={busy}
                    onClick={() => respond.mutate({ account, accept: false })}
                    type="button"
                  >
                    Decline
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      ) : null}
    </section>
  );
}
