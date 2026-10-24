import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import type { SliceRef } from "../../api/types";
import { useApi } from "../../api/useApi";
import { AccountLink } from "../AccountAvatar";
import { SliceLoadingBlock, SliceNotice, SlicePanel, getErrorMessage } from "./SlicePageParts";

export const SLICE_ROLES: { role: string; help: string }[] = [
  { role: "reader", help: "read and clone the slice" },
  { role: "writer", help: "also create, submit and approve changesets" },
  { role: "admin", help: "also manage the slice's members and webhooks" }
];

const inputClass =
  "h-9 min-w-0 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:focus:ring-zinc-700";
const buttonClass =
  "rounded-md border border-slate-200 px-2.5 py-1 text-xs font-medium text-slate-700 transition hover:bg-slate-50 disabled:opacity-50 dark:border-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-950";

/**
 * Who the slice is shared with beyond its account (design/26_slice_members.md),
 * for the account's owners and admins: add a person or agent by username with
 * a role, change it, or remove them. The server checks too.
 */
export function SliceMembersPanel({ slice }: { slice: SliceRef }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const queryKey = ["sliceMembers", slice.account, slice.slice];
  const [username, setUsername] = useState("");
  const [role, setRole] = useState("writer");
  const [error, setError] = useState("");

  const membersQuery = useQuery({
    queryKey,
    queryFn: async () => (await api.listSliceMembers({ slice })).members ?? []
  });
  const refresh = () => queryClient.invalidateQueries({ queryKey });
  const onError = (err: unknown) => setError(getErrorMessage(err));

  const setMember = useMutation({
    mutationFn: (input: { username: string; role: string }) => api.setSliceMember({ slice, ...input }),
    onError,
    onSuccess: async () => {
      setError("");
      setUsername("");
      await refresh();
    }
  });
  const removeMember = useMutation({
    mutationFn: (name: string) => api.removeSliceMember({ slice, username: name }),
    onError,
    onSuccess: async () => {
      setError("");
      await refresh();
    }
  });
  const busy = setMember.isPending || removeMember.isPending;

  function submit(event: FormEvent) {
    event.preventDefault();
    if (username.trim()) {
      setMember.mutate({ role, username: username.trim() });
    }
  }

  const members = membersQuery.data ?? [];
  return (
    <SlicePanel>
      <div className="px-4 py-4 sm:px-5">
        <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">People</h2>
        <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Share this slice with a person or agent outside {slice.account}. They get the role on this slice only;
          the members of {slice.account} keep their access.
        </p>
        <form className="mt-4 flex flex-wrap items-center gap-2" onSubmit={submit}>
          <input
            aria-label="Username"
            className={inputClass}
            onChange={(event) => setUsername(event.target.value)}
            placeholder="username"
            value={username}
          />
          <select aria-label="Role" className={inputClass} onChange={(event) => setRole(event.target.value)} value={role}>
            {SLICE_ROLES.map((item) => (
              <option key={item.role} title={item.help} value={item.role}>
                {item.role}
              </option>
            ))}
          </select>
          <button className={buttonClass} disabled={busy || !username.trim()} type="submit">
            Share
          </button>
        </form>
        {error ? (
          <p className="mt-2 text-sm text-rose-700 dark:text-rose-300" role="alert">
            {error}
          </p>
        ) : null}
        <div className="mt-4">
          {membersQuery.isLoading ? (
            <SliceLoadingBlock />
          ) : membersQuery.isError ? (
            <SliceNotice title="Could not load people" tone="error">
              {getErrorMessage(membersQuery.error)}
            </SliceNotice>
          ) : members.length === 0 ? (
            <p className="text-sm text-slate-500 dark:text-zinc-400">Not shared with anyone outside {slice.account}.</p>
          ) : (
            <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 dark:divide-zinc-800 dark:border-zinc-800">
              {members.map((member) => {
                const name = member.username || member.subjectId || "unknown";
                return (
                  <li className="flex flex-wrap items-center gap-3 px-4 py-3" key={member.subjectId || name}>
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
                    <select
                      aria-label={`Role of ${name}`}
                      className={inputClass}
                      disabled={busy || !member.username}
                      onChange={(event) => setMember.mutate({ role: event.target.value, username: member.username ?? "" })}
                      value={member.role ?? ""}
                    >
                      {SLICE_ROLES.map((item) => (
                        <option key={item.role} value={item.role}>
                          {item.role}
                        </option>
                      ))}
                    </select>
                    <button
                      className={buttonClass}
                      disabled={busy || !member.username}
                      onClick={() => removeMember.mutate(member.username ?? "")}
                      type="button"
                    >
                      Remove
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      </div>
    </SlicePanel>
  );
}
