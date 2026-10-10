import { useAuth } from "@clerk/tanstack-react-start";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useParams } from "@tanstack/react-router";

import { RpcError } from "../api/client";
import type { AccountProfile, ListSlicesResponse } from "../api/types";
import { useApi } from "../api/useApi";
import { AccountAvatar } from "../components/AccountSwitcher";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import { AccountPeople } from "../components/slices/AccountPeople";
import {
  SliceLoadingBlock,
  SliceNotice,
  VisibilityBadge,
  formatPathPreview,
  getErrorMessage
} from "../components/slices/SlicePageParts";
import { canAdmin, kindLabel, membershipFor, type Membership } from "../lib/accounts";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { useSelection } from "../state/selection";

const PAGE_SIZE = 50;

function isDefinite(error: unknown) {
  return error instanceof RpcError && [400, 401, 403, 404].includes(error.status);
}

function isNotFound(error: unknown) {
  return (
    error instanceof RpcError &&
    (error.status === 404 || error.code === 5 || error.code === "5" || error.code === "NotFound" || error.code === "not_found")
  );
}

// An account's page, personal or organization: its slices that the visitor can
// open, and for the account's members its people. Anyone sees the public
// slices; members see every slice.
export function AccountPage() {
  const api = useApi();
  const { isLoaded } = useAuth();
  const { memberships, activeAccount, setActiveAccount } = useSelection();
  const params = useParams({ strict: false }) as { account?: string };
  const account = params.account ?? "";
  // The viewer's place in this account, if they belong to it.
  const membership = membershipFor(memberships, account);

  const slicesQuery = useInfiniteQuery({
    // Wait for Clerk, so a member's token is attached and their private slices
    // are included on the first request.
    enabled: Boolean(isLoaded && account),
    getNextPageParam: (last: ListSlicesResponse) => last.nextCursor || undefined,
    initialPageParam: "",
    queryKey: ["accountSlices", account, membership?.role ?? ""],
    queryFn: ({ pageParam }) => api.listSlices({ account, cursor: pageParam, pageSize: PAGE_SIZE }),
    // Asking again does not change a definite answer (no such account, not
    // signed in, not allowed); only failures that may pass are retried.
    retry: (count, error) => !isDefinite(error) && count < 2
  });
  const profileQuery = useQuery({
    enabled: Boolean(account),
    queryKey: ["accountProfile", account],
    queryFn: () => api.getAccountProfile({ account }),
    retry: (count, error) => !isDefinite(error) && count < 2
  });
  const profile = profileQuery.data;
  const pages = slicesQuery.data?.pages ?? [];
  const slices = pages.flatMap((page: ListSlicesResponse) => page.slices ?? []);
  const kind = membership?.kind || profile?.kind || pages[0]?.accountKind || "";
  const notFound = isNotFound(slicesQuery.error) || isNotFound(profileQuery.error);
  // An organization's owners and admins edit its profile; a person edits their own.
  const canEditProfile =
    membership?.kind === "personal" || (membership?.kind === "organization" && canAdmin(membership));
  const [editing, setEditing] = useState(false);

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader breadcrumb={<Breadcrumb items={[{ label: "Home", to: "/" }, { label: `@${account}` }]} />} />
      {notFound ? (
        <div className="mt-4">
          <SliceNotice title="No such account">There is no account named {account}.</SliceNotice>
        </div>
      ) : (
        <>
          <div className="mt-4 flex flex-wrap items-center gap-4">
            <AccountAvatar account={account} kind={kind} size="lg" />
            <div className="min-w-0 flex-1">
              <h1 className="truncate text-xl font-semibold text-zinc-950 dark:text-zinc-50">
                {profile?.displayName || account}
              </h1>
              <p className="text-sm text-slate-600 dark:text-zinc-400">
                {profile?.displayName ? `@${account} · ` : ""}
                {kind ? kindLabel(kind) : "Account"}
                {membership?.role ? ` · you are ${article(membership.role)} ${membership.role}` : ""}
              </p>
            </div>
            {canEditProfile && !editing ? (
              <button
                className="rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
                onClick={() => setEditing(true)}
                type="button"
              >
                Edit profile
              </button>
            ) : null}
            {membership && membership.account !== activeAccount ? (
              <button
                className="rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
                onClick={() => setActiveAccount(membership.account)}
                type="button"
              >
                Switch to {account}
              </button>
            ) : null}
            {canAdmin(membership) ? (
              <Link
                className="rounded-md bg-zinc-950 px-3 py-1.5 text-sm font-semibold text-white transition hover:bg-zinc-800 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
                search={{ account } as never}
                to="/slices/new"
              >
                New slice
              </Link>
            ) : null}
          </div>

          {editing && profile ? (
            <ProfileEditor account={account} onDone={() => setEditing(false)} profile={profile} />
          ) : profile?.description || profile?.website ? (
            <div className="mt-4 max-w-3xl space-y-1 text-sm leading-6 text-slate-700 dark:text-zinc-300">
              {profile.description ? <p className="whitespace-pre-line">{profile.description}</p> : null}
              {profile.website ? (
                <a
                  className="inline-block break-all text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                  href={profile.website}
                  rel="noopener noreferrer nofollow ugc"
                  target="_blank"
                >
                  {profile.website.replace(/^https?:\/\//, "")}
                </a>
              ) : null}
            </div>
          ) : null}

          <h2 className="mb-2 mt-8 text-sm font-semibold text-zinc-950 dark:text-zinc-50">Slices</h2>
          <p className="mb-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            {membership?.kind === "personal"
              ? "Your slices. Others see only the public ones."
              : membership
                ? `Slices of ${account}, which you belong to. Others see only the public ones.`
                : `Public slices owned by ${account}.`}
          </p>
          {slicesQuery.isPending ? (
            <SliceLoadingBlock />
          ) : slicesQuery.isError ? (
            <SliceNotice title="Could not load slices" tone="error">
              {getErrorMessage(slicesQuery.error)}
            </SliceNotice>
          ) : slices.length === 0 ? (
            <SliceNotice title={membership ? "No slices yet" : "No public slices"}>
              {membership ? `${account} has no slices yet.` : `${account} has no public slices.`}
            </SliceNotice>
          ) : (
            <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/50">
              {slices.map((slice) => {
                const routeParams = toSliceRouteParams(slice.ref);
                const name = slice.ref?.slice ?? slice.id ?? "slice";
                const paths = slice.definition?.includedPaths ?? [];
                const body = (
                  <>
                    <span className="min-w-0">
                      <span className="block truncate font-medium text-zinc-950 dark:text-zinc-50">{name}</span>
                      {paths.length > 0 ? (
                        <span className="block truncate text-xs text-slate-500 dark:text-zinc-400">{formatPathPreview(paths)}</span>
                      ) : null}
                    </span>
                    <VisibilityBadge visibility={slice.definition?.visibility} />
                  </>
                );
                return (
                  <li key={slice.id ?? name}>
                    {routeParams ? (
                      <Link
                        className="flex items-center justify-between gap-3 px-4 py-3 transition hover:bg-slate-50 dark:hover:bg-zinc-950"
                        params={routeParams}
                        to="/slices/$account/$slice"
                      >
                        {body}
                      </Link>
                    ) : (
                      <div className="flex items-center justify-between gap-3 px-4 py-3">{body}</div>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
          {slicesQuery.hasNextPage ? (
            <div className="mt-4 flex justify-center">
              <button
                className="rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 transition hover:bg-slate-50 disabled:opacity-60 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
                disabled={slicesQuery.isFetchingNextPage}
                onClick={() => void slicesQuery.fetchNextPage()}
                type="button"
              >
                {slicesQuery.isFetchingNextPage ? "Loading…" : "Show more"}
              </button>
            </div>
          ) : null}

          {membership?.kind === "personal" ? <YourAccounts memberships={memberships} /> : null}

          {membership && kind === "organization" ? <AccountPeople account={account} membership={membership} /> : null}
        </>
      )}
    </section>
  );
}

// On your own page: the organizations you belong to and the agents you have
// claimed, each with your role, like a GitHub profile's organizations.
function YourAccounts({ memberships }: { memberships: readonly Membership[] }) {
  const groups = [
    { kind: "organization", title: "Organizations", empty: "You do not belong to any organizations yet." },
    { kind: "agent", title: "Agents", empty: "" }
  ];
  return (
    <>
      {groups.map(({ empty, kind, title }) => {
        const accounts = memberships.filter((m) => m.kind === kind);
        if (accounts.length === 0 && !empty) {
          return null;
        }
        return (
          <section aria-label={title} className="mt-10" key={kind}>
            <div className="mb-2 flex items-center justify-between gap-3">
              <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">{title}</h2>
              {kind === "organization" ? (
                <Link
                  className="rounded-md border border-slate-300 bg-white px-2.5 py-1 text-xs font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
                  to="/organizations/new"
                >
                  New organization
                </Link>
              ) : null}
            </div>
            {accounts.length === 0 ? (
              <p className="text-sm text-slate-600 dark:text-zinc-400">{empty}</p>
            ) : (
              <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900">
                {accounts.map((m) => (
                  <li key={m.account}>
                    <Link
                      className="flex items-center gap-3 px-4 py-3 transition hover:bg-slate-50 dark:hover:bg-zinc-950"
                      params={{ account: m.account } as never}
                      to="/accounts/$account"
                    >
                      <AccountAvatar account={m.account} kind={m.kind} />
                      <span className="min-w-0 flex-1 truncate text-sm font-medium text-zinc-950 dark:text-zinc-50">{m.account}</span>
                      {m.role ? <span className="text-xs text-slate-500 dark:text-zinc-400">{m.role}</span> : null}
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>
        );
      })}
    </>
  );
}

function article(role: string) {
  return /^[aeiou]/.test(role) ? "an" : "a";
}

// ProfileEditor edits an account's display name, description and website.
function ProfileEditor({ account, onDone, profile }: { account: string; onDone(): void; profile: AccountProfile }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [displayName, setDisplayName] = useState(profile.displayName ?? "");
  const [description, setDescription] = useState(profile.description ?? "");
  const [website, setWebsite] = useState(profile.website ?? "");
  const save = useMutation({
    mutationFn: () => api.updateAccountProfile({ account, description, displayName, website }),
    onSuccess: (updated) => {
      queryClient.setQueryData(["accountProfile", account], updated);
      onDone();
    }
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    save.mutate();
  }

  const field =
    "rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none focus:border-slate-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50";
  return (
    <form aria-label="Edit profile" className="mt-4 grid max-w-xl gap-3" onSubmit={submit}>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Display name
        <input className={field} maxLength={64} onChange={(e) => setDisplayName(e.target.value)} value={displayName} />
      </label>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Description
        <textarea className={field} maxLength={280} onChange={(e) => setDescription(e.target.value)} rows={3} value={description} />
      </label>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Website
        <input className={field} onChange={(e) => setWebsite(e.target.value)} placeholder="https://" type="url" value={website} />
      </label>
      {save.isError ? (
        <SliceNotice title="Could not save the profile" tone="error">
          {getErrorMessage(save.error)}
        </SliceNotice>
      ) : null}
      <div className="flex gap-2">
        <button
          className="rounded-md bg-zinc-950 px-3 py-1.5 text-sm font-semibold text-white transition hover:bg-zinc-800 disabled:opacity-60 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
          disabled={save.isPending}
          type="submit"
        >
          {save.isPending ? "Saving..." : "Save profile"}
        </button>
        <button
          className="rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium text-slate-700 dark:border-zinc-700 dark:text-zinc-300"
          disabled={save.isPending}
          onClick={onDone}
          type="button"
        >
          Cancel
        </button>
      </div>
    </form>
  );
}
