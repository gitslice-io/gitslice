import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import { useApi } from "../../api/useApi";
import { canAdmin, type Membership } from "../../lib/accounts";
import { AccountAvatar, AccountLink } from "../AccountAvatar";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Select } from "../ui/Select";
import { SliceLoadingBlock, SliceNotice, getErrorMessage } from "./SlicePageParts";
import { accountInvitationsKey, accountMembersKey } from "../../queries/accounts";
import { authStatusKey } from "../../queries/auth";

const ROLES = ["owner", "admin", "writer", "member", "contributor", "reader"] as const;

const ROLE_HELP: Record<string, string> = {
  admin: "manages members and slices",
  contributor: "can propose changesets",
  member: "can change files",
  owner: "full control",
  reader: "can read private slices",
  writer: "can change files"
};

// An organization's people, for its members. Owners and admins invite people
// (who join when they accept), change roles and remove people; the server
// enforces who may do what (admins cannot make or remove owners, and the last
// owner stays).
export function AccountPeople({ account, membership }: { account: string; membership: Membership }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const manage = canAdmin(membership);
  const [username, setUsername] = useState("");
  const [role, setRole] = useState<string>("writer");
  const [error, setError] = useState("");

  const membersQuery = useQuery({
    queryKey: accountMembersKey(account),
    queryFn: () => api.listAccountMembers({ account })
  });
  const invitationsQuery = useQuery({
    enabled: manage,
    queryKey: accountInvitationsKey(account),
    queryFn: async () => (await api.listAccountInvitations({ account })).invitations ?? []
  });

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: accountMembersKey(account) }),
      queryClient.invalidateQueries({ queryKey: accountInvitationsKey(account) }),
      // Your own role may have changed.
      queryClient.invalidateQueries({ queryKey: authStatusKey })
    ]);
  };

  const invite = useMutation({
    mutationFn: (input: { username: string; role: string }) => api.inviteAccountMember({ account, ...input }),
    onError: (err) => setError(getErrorMessage(err)),
    onMutate: () => setError(""),
    onSuccess: refresh
  });
  const cancelInvite = useMutation({
    mutationFn: (name: string) => api.cancelAccountInvitation({ account, username: name }),
    onError: (err) => setError(getErrorMessage(err)),
    onMutate: () => setError(""),
    onSuccess: refresh
  });

  const setMember = useMutation({
    mutationFn: (input: { username: string; role: string }) => api.setAccountMember({ account, ...input }),
    onError: (err) => setError(getErrorMessage(err)),
    onMutate: () => setError(""),
    onSuccess: refresh
  });
  const removeMember = useMutation({
    mutationFn: (name: string) => api.removeAccountMember({ account, username: name }),
    onError: (err) => setError(getErrorMessage(err)),
    onMutate: () => setError(""),
    onSuccess: refresh
  });

  function add(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const name = username.trim().replace(/^@/, "");
    if (!name) {
      return;
    }
    invite.mutate({ role, username: name }, { onSuccess: () => setUsername("") });
  }

  const members = membersQuery.data?.members ?? [];
  const busy = setMember.isPending || removeMember.isPending || invite.isPending || cancelInvite.isPending;
  const invitations = invitationsQuery.data ?? [];

  return (
    <section aria-label="People" className="mt-10">
      <h2 className="mb-2 text-sm font-semibold text-zinc-950 dark:text-zinc-50">People</h2>
      <p className="mb-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
        {manage
          ? "Members of this organization and their roles. Invite people by username; they join when they accept."
          : "Members of this organization and their roles."}
      </p>
      {error ? (
        <div className="mb-4">
          <SliceNotice title="Could not update people" tone="error">
            {error}
          </SliceNotice>
        </div>
      ) : null}
      {membersQuery.isPending ? (
        <SliceLoadingBlock />
      ) : membersQuery.isError ? (
        <SliceNotice title="Could not load people" tone="error">
          {getErrorMessage(membersQuery.error)}
        </SliceNotice>
      ) : (
        <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900">
          {members.map((member) => {
            const name = member.username || member.subjectId || "unknown";
            return (
              <li className="flex flex-wrap items-center gap-3 px-4 py-3" key={member.subjectId || name}>
                <AccountAvatar account={name} kind="personal" />
                <span className="min-w-0 flex-1 truncate text-sm font-medium text-zinc-950 dark:text-zinc-50">
                  <AccountLink account={name} />
                  {member.displayName ? (
                    <span className="ml-2 font-normal text-slate-500 dark:text-zinc-400">{member.displayName}</span>
                  ) : null}
                  {member.kind === "agent" ? (
                    <span className="ml-2 rounded bg-sky-50 px-1.5 py-0.5 text-xs font-normal text-sky-800 dark:bg-sky-950/40 dark:text-sky-200">
                      agent
                    </span>
                  ) : null}
                </span>
                {manage && member.username ? (
                  <>
                    <Select
                      aria-label={`Role of ${name}`}
                      disabled={busy}
                      onChange={(event) => setMember.mutate({ role: event.target.value, username: member.username ?? "" })}
                      size="sm"
                      value={member.role ?? ""}
                    >
                      {ROLES.map((option) => (
                        <option key={option} value={option}>
                          {option}
                        </option>
                      ))}
                    </Select>
                    <Button
                      disabled={busy}
                      onClick={() => removeMember.mutate(member.username ?? "")}
                      variant="danger"
                    >
                      Remove
                    </Button>
                  </>
                ) : (
                  <span className="text-xs text-slate-500 dark:text-zinc-400" title={ROLE_HELP[member.role ?? ""]}>
                    {member.role}
                  </span>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {manage && invitations.length > 0 ? (
        <div className="mt-6">
          <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">Pending invitations</h3>
          <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-dashed border-slate-300 bg-white dark:divide-zinc-800 dark:border-zinc-700 dark:bg-zinc-900">
            {invitations.map((invitation) => (
              <li className="flex flex-wrap items-center gap-3 px-4 py-2.5" key={invitation.username}>
                <AccountAvatar account={invitation.username ?? "?"} kind="personal" />
                <span className="min-w-0 flex-1 truncate text-sm text-zinc-950 dark:text-zinc-50">
                  <AccountLink account={invitation.username ?? ""} />
                  <span className="ml-2 text-xs text-slate-500 dark:text-zinc-400">
                    invited as {invitation.role}
                    {invitation.invitedBy ? (
                      <>
                        {" by "}
                        <AccountLink account={invitation.invitedBy} />
                      </>
                    ) : null}
                  </span>
                </span>
                <Button
                  disabled={busy}
                  onClick={() => cancelInvite.mutate(invitation.username ?? "")}
                >
                  Cancel invitation
                </Button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {manage ? (
        <form className="mt-4 flex flex-wrap items-end gap-3" onSubmit={add}>
          <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
            Invite a person by username
            <Input
              className="w-56"
              disabled={busy}
              monospace
              onChange={(event) => setUsername(event.target.value)}
              placeholder="username"
              spellCheck={false}
              value={username}
            />
          </label>
          <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
            Role
            <Select
              disabled={busy}
              onChange={(event) => setRole(event.target.value)}
              value={role}
            >
              {ROLES.map((option) => (
                <option key={option} value={option}>
                  {option} — {ROLE_HELP[option]}
                </option>
              ))}
            </Select>
          </label>
          <Button
            className="h-9"
            disabled={busy || username.trim() === ""}
            size="md"
            type="submit"
            variant="primary"
          >
            Invite
          </Button>
        </form>
      ) : null}
    </section>
  );
}
