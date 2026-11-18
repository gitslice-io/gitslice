import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import type { SliceRef } from "../../api/types";
import { useApi } from "../../api/useApi";
import { sharedSlicesKey } from "../../queries/auth";
import { AccountLink } from "../AccountAvatar";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Select } from "../ui/Select";
import { SliceLoadingBlock, SliceNotice, SlicePanel, getErrorMessage } from "./SlicePageParts";

export const SLICE_ROLES: { role: string; help: string }[] = [
  { role: "reader", help: "read and clone the slice" },
  { role: "contributor", help: "also create changesets and push patchsets" },
  { role: "writer", help: "also submit and approve changesets" },
  { role: "admin", help: "also manage the slice's members" }
];

/**
 * Who the slice is shared with beyond its account (design/26 & design/36),
 * for the slice's and account's admins: add a person, agent, or @account/group
 * with a role, change it, or remove them. The server checks too.
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
    queryFn: () => api.listSliceMembers({ slice })
  });
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey }),
      queryClient.invalidateQueries({ queryKey: sharedSlicesKey })
    ]);
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

  const members = membersQuery.data?.members ?? [];
  const groupMembers = membersQuery.data?.groupMembers ?? [];
  const isEmpty = members.length === 0 && groupMembers.length === 0;
  return (
    <SlicePanel>
      <div className="px-4 py-4 sm:px-5">
        <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">People</h2>
        <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Share this slice with a person, agent, or <code>@account/group</code> outside {slice.account}. They get the
          role on this slice only; the members of {slice.account} keep their access.
        </p>
        <form className="mt-4 flex flex-wrap items-center gap-2" onSubmit={submit}>
          <Input
            aria-label="Username"
            onChange={(event) => setUsername(event.target.value)}
            placeholder="username or @account/group"
            value={username}
          />
          <Select aria-label="Role" onChange={(event) => setRole(event.target.value)} value={role}>
            {SLICE_ROLES.map((item) => (
              <option key={item.role} title={item.help} value={item.role}>
                {item.role}
              </option>
            ))}
          </Select>
          <Button disabled={busy || !username.trim()} type="submit">
            Share
          </Button>
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
          ) : isEmpty ? (
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
                    <Select
                      aria-label={`Role of ${name}`}
                      disabled={busy || !member.username}
                      onChange={(event) => setMember.mutate({ role: event.target.value, username: member.username ?? "" })}
                      value={member.role ?? ""}
                    >
                      {SLICE_ROLES.map((item) => (
                        <option key={item.role} value={item.role}>
                          {item.role}
                        </option>
                      ))}
                    </Select>
                    <Button
                      disabled={busy || !member.username}
                      onClick={() => removeMember.mutate(member.username ?? "")}
                    >
                      Remove
                    </Button>
                  </li>
                );
              })}
              {groupMembers.map((group) => {
                const handle =
                  group.handle ||
                  (group.account && group.name ? `@${group.account}/${group.name}` : "");
                const label = handle || group.groupId || "unknown";
                return (
                  <li className="flex flex-wrap items-center gap-3 px-4 py-3" key={group.groupId || label}>
                    <span className="min-w-0 flex-1 truncate font-mono text-sm font-medium text-zinc-950 dark:text-zinc-50">
                      {label}
                      <span className="ml-2 rounded bg-slate-100 px-1.5 py-0.5 font-sans text-xs font-normal text-slate-700 dark:bg-zinc-800 dark:text-zinc-300">
                        group
                      </span>
                    </span>
                    <Select
                      aria-label={`Role of ${label}`}
                      disabled={busy || !handle}
                      onChange={(event) => setMember.mutate({ role: event.target.value, username: handle })}
                      value={group.role ?? ""}
                    >
                      {SLICE_ROLES.map((item) => (
                        <option key={item.role} value={item.role}>
                          {item.role}
                        </option>
                      ))}
                    </Select>
                    <Button
                      disabled={busy || !handle}
                      onClick={() => removeMember.mutate(handle)}
                    >
                      Remove
                    </Button>
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
