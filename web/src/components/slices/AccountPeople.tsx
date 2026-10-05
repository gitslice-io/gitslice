import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import { useApi } from "../../api/useApi";
import { canAdmin, type Membership } from "../../lib/accounts";
import { AccountAvatar } from "../AccountSwitcher";
import { SliceLoadingBlock, SliceNotice, getErrorMessage } from "./SlicePageParts";

const ROLES = ["owner", "admin", "writer", "member", "reader"] as const;

const ROLE_HELP: Record<string, string> = {
  admin: "manages members and slices",
  member: "can change files",
  owner: "full control",
  reader: "can read private slices",
  writer: "can change files"
};

// An organization's people, for its members. Owners and admins can add people,
// change their roles and remove them; the server enforces who may do what
// (admins cannot make or remove owners, and the last owner stays).
export function AccountPeople({ account, membership }: { account: string; membership: Membership }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const manage = canAdmin(membership);
  const [username, setUsername] = useState("");
  const [role, setRole] = useState<string>("writer");
  const [error, setError] = useState("");

  const membersQuery = useQuery({
    queryKey: ["accountMembers", account],
    queryFn: () => api.listAccountMembers({ account })
  });

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: ["accountMembers", account] });
    // Your own role may have changed.
    await queryClient.invalidateQueries({ queryKey: ["authStatus"] });
  };

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
    setMember.mutate({ role, username: name }, { onSuccess: () => setUsername("") });
  }

  const members = membersQuery.data?.members ?? [];
  const busy = setMember.isPending || removeMember.isPending;

  return (
    <section aria-label="People" className="mt-10">
      <h2 className="mb-2 text-sm font-semibold text-zinc-950 dark:text-zinc-50">People</h2>
      <p className="mb-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
        {manage ? "Members of this organization and their roles. You can manage them." : "Members of this organization and their roles."}
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
                  {name}
                </span>
                {manage && member.username ? (
                  <>
                    <select
                      aria-label={`Role of ${name}`}
                      className="h-8 rounded-md border border-slate-300 bg-white px-2 text-sm dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
                      disabled={busy}
                      onChange={(event) => setMember.mutate({ role: event.target.value, username: member.username ?? "" })}
                      value={member.role ?? ""}
                    >
                      {ROLES.map((option) => (
                        <option key={option} value={option}>
                          {option}
                        </option>
                      ))}
                    </select>
                    <button
                      className="rounded-md border border-slate-200 px-2.5 py-1 text-xs font-medium text-rose-700 transition hover:bg-rose-50 disabled:opacity-50 dark:border-zinc-800 dark:text-rose-300 dark:hover:bg-rose-950/30"
                      disabled={busy}
                      onClick={() => removeMember.mutate(member.username ?? "")}
                      type="button"
                    >
                      Remove
                    </button>
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
      {manage ? (
        <form className="mt-4 flex flex-wrap items-end gap-3" onSubmit={add}>
          <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
            Add a person by username
            <input
              className="h-9 w-56 rounded-md border border-slate-300 bg-white px-3 font-mono text-sm text-zinc-950 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50"
              disabled={busy}
              onChange={(event) => setUsername(event.target.value)}
              placeholder="username"
              spellCheck={false}
              value={username}
            />
          </label>
          <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
            Role
            <select
              className="h-9 rounded-md border border-slate-300 bg-white px-2 text-sm dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
              disabled={busy}
              onChange={(event) => setRole(event.target.value)}
              value={role}
            >
              {ROLES.map((option) => (
                <option key={option} value={option}>
                  {option} — {ROLE_HELP[option]}
                </option>
              ))}
            </select>
          </label>
          <button
            className="h-9 rounded-md bg-zinc-950 px-3 text-sm font-semibold text-white transition hover:bg-zinc-800 disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
            disabled={busy || username.trim() === ""}
            type="submit"
          >
            Add
          </button>
        </form>
      ) : null}
    </section>
  );
}
