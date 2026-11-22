import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { Link, useParams } from "@tanstack/react-router";

import { isDefiniteClientError, isNotFoundError } from "../api/rpcError";
import type { AccountProfile, ListSlicesResponse } from "../api/types";
import { useApi } from "../api/useApi";
import { useAppAuth } from "../auth/AppAuthProvider";
import { accountProfileKey, accountProfileQuery, accountSlicesQuery } from "../queries/accounts";
import { AccountAvatar, rememberAvatarVersion } from "../components/AccountAvatar";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import { AccountCIDaemonPanel } from "../components/slices/AccountCIDaemonPanel";
import { AccountPeople } from "../components/slices/AccountPeople";
import { AgentDaemons } from "../components/slices/AgentDaemons";
import { OwnedAgents } from "../components/slices/OwnedAgents";
import {
  SliceLoadingBlock,
  SliceNotice,
  VisibilityBadge,
  formatPathPreview,
  getErrorMessage
} from "../components/slices/SlicePageParts";
import { Timestamp } from "../components/Timestamp";
import { Button } from "../components/ui/Button";
import { Input, inputClassName } from "../components/ui/Input";
import { canAdmin, kindLabel, membershipFor, type Membership } from "../lib/accounts";
import { isHomeSlice, isSliceSort, loadSliceSort, saveSliceSort, sliceSorts, sortSlices, type SliceSort } from "../lib/sliceOrder";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { useSelection } from "../state/selection";

// An account's page, personal or organization: its slices that the visitor can
// open, and for the account's members its people. Anyone sees the public
// slices; members see every slice.
export function AccountPage() {
  const api = useApi();
  const { authKind, isLoaded } = useAppAuth();
  const { memberships, activeAccount, setActiveAccount } = useSelection();
  const params = useParams({ from: "/publicApp/accounts/$account" });
  const account = params.account ?? "";
  // The viewer's place in this account, if they belong to it.
  const membership = membershipFor(memberships, account);
  const [sort, setSort] = useState<SliceSort>("name");
  const [sortReady, setSortReady] = useState(false);
  // Read after mount, so the server render and the first client render agree,
  // and wait before firing listSlices so a saved non-default sort doesn't
  // waste an initial name-sorted request.
  useEffect(() => {
    setSort(loadSliceSort());
    setSortReady(true);
  }, []);

  const slicesQuery = useInfiniteQuery({
    ...accountSlicesQuery(api, account, membership?.role ?? "", sort),
    // Wait for Clerk, so a member's token is attached and their private slices
    // are included on the first request.
    enabled: Boolean(isLoaded && sortReady && account),
    // Asking again does not change a definite answer (no such account, not
    // signed in, not allowed); only failures that may pass are retried.
    retry: (count, error) => !isDefiniteClientError(error) && count < 2
  });
  const profileQuery = useQuery({
    ...accountProfileQuery(api, account),
    enabled: Boolean(account),
    retry: (count, error) => !isDefiniteClientError(error) && count < 2
  });
  const profile = profileQuery.data;
  // Every avatar of this account on the page uses the picture the profile
  // names, so an upload shows everywhere at once.
  useEffect(() => {
    if (profile?.account) {
      rememberAvatarVersion(profile.account, profile.avatarVersion ?? "");
    }
  }, [profile?.account, profile?.avatarVersion]);
  const pages = slicesQuery.data?.pages ?? [];
  const slices = sortSlices(
    pages.flatMap((page: ListSlicesResponse) => page.slices ?? []),
    sort
  );
  const homeSlice = slices.find((slice) => isHomeSlice(slice));
  const viewSlices = homeSlice ? slices.filter((slice) => slice !== homeSlice) : slices;
  const homeRouteParams = homeSlice ? toSliceRouteParams(homeSlice.ref) : undefined;
  const kind = membership?.kind || profile?.kind || pages[0]?.accountKind || "";
  const displayKind = authKind === "agent" && membership?.kind === "personal" ? "agent" : kind;
  const notFound = isNotFoundError(slicesQuery.error) || isNotFoundError(profileQuery.error);
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
          <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-3">
            <AccountAvatar account={account} kind={displayKind} size="xl" version={profile ? (profile.avatarVersion ?? "") : undefined} />
            {/* At least this wide, so the buttons wrap below it on a phone
                instead of squeezing the name into a sliver. */}
            <div className="min-w-[12rem] flex-1">
              <h1 className="truncate text-xl font-semibold text-zinc-950 dark:text-zinc-50">
                {profile?.displayName || account}
              </h1>
              <p className="text-sm text-slate-600 dark:text-zinc-400">
                {profile?.displayName ? `@${account} · ` : ""}
                {displayKind ? kindLabel(displayKind) : "Account"}
                {membership?.role ? ` · you are ${article(membership.role)} ${membership.role}` : ""}
              </p>
            </div>
            <div className="flex basis-full flex-wrap items-center gap-2 sm:basis-auto">
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
                className="rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
                params={{ account }}
                to="/accounts/$account/secrets"
              >
                Secrets
              </Link>
            ) : null}
            {canAdmin(membership) ? (
              <Link
                className="rounded-md bg-zinc-950 px-3 py-1.5 text-sm font-semibold text-white transition hover:bg-zinc-800 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
                search={{ account }}
                to="/slices/new"
              >
                New slice
              </Link>
            ) : null}
            </div>
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

          {homeSlice && homeRouteParams ? (
            <section
              aria-label="Monorepo"
              className="mt-8 rounded-lg border border-slate-200 bg-white p-4 shadow-sm shadow-slate-200/50 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/50 sm:p-5"
            >
              <div className="flex flex-wrap items-start justify-between gap-4">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Monorepo</h2>
                    <span className="rounded border border-slate-200 bg-slate-50 px-1.5 py-px font-mono text-xs font-semibold text-slate-700 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-200">
                      /{account}
                    </span>
                    <VisibilityBadge visibility={homeSlice.definition?.visibility} />
                  </div>
                  <p className="mt-1.5 max-w-2xl text-sm leading-6 text-slate-600 dark:text-zinc-400">
                    One unified source tree for everything in <span className="font-mono">/{account}</span>. Checks, triggers,
                    path owners, and secrets live on folders inside it.
                  </p>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Link
                    className="rounded-md bg-zinc-950 px-3 py-1.5 text-sm font-semibold text-white transition hover:bg-zinc-800 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
                    params={homeRouteParams}
                    to="/slices/$account/$slice"
                  >
                    Browse /{account}
                  </Link>
                  <Link
                    className="rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
                    search={{ slice: `${account}:${homeRouteParams.slice}` }}
                    to="/changesets"
                  >
                    Changesets
                  </Link>
                  <Link
                    className="rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
                    params={homeRouteParams}
                    search={{ policy: true }}
                    to="/slices/$account/$slice"
                  >
                    Folder policy
                  </Link>
                </div>
              </div>
            </section>
          ) : null}

          <div className="mb-2 mt-8 flex items-center justify-between gap-3">
            <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
              {homeSlice ? "Slices (Views)" : "Slices"}
            </h2>
            <label className="flex items-center gap-2 text-xs text-slate-500 dark:text-zinc-400">
              Sort
              <select
                className="h-8 rounded-md border border-slate-300 bg-white px-2 text-sm text-zinc-950 outline-none focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:focus:ring-zinc-700"
                onChange={(event) => {
                  if (isSliceSort(event.target.value)) {
                    setSort(event.target.value);
                    saveSliceSort(event.target.value);
                  }
                }}
                value={sort}
              >
                {sliceSorts.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <p className="mb-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            {membership?.kind === "personal"
              ? `Focused views, checkouts, access boundaries, and Git mirrors over /${account}. Others see only the public ones.`
              : membership
                ? `Slices of ${account}, which you belong to — focused views, access boundaries, and Git mirrors over /${account}. Others see only the public ones.`
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
          ) : viewSlices.length === 0 ? (
            <SliceNotice title="No additional slice views yet">
              Browse everything in <span className="font-mono">/{account}</span> above, or create a slice view to scope checkouts,
              share specific folders, or mirror a folder to Git.
            </SliceNotice>
          ) : (
            <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/50">
              {viewSlices.map((slice) => {
                const routeParams = toSliceRouteParams(slice.ref);
                const name = slice.ref?.slice ?? slice.id ?? "slice";
                const paths = slice.definition?.includedPaths ?? [];
                // The date the list is sorted by, when it is sorted by one.
                const when = sort === "active" ? slice.lastActivityAt || slice.createdAt : sort === "newest" ? slice.createdAt : "";
                const body = (
                  <>
                    <span className="min-w-0">
                      <span className="flex items-center gap-2">
                        <span className="truncate font-medium text-zinc-950 dark:text-zinc-50">{name}</span>
                      </span>
                      {paths.length > 0 ? (
                        <span className="block truncate font-mono text-xs text-slate-500 dark:text-zinc-400">
                          {formatPathPreview(paths)}
                        </span>
                      ) : null}
                      {when ? (
                        <span className="block text-xs text-slate-500 dark:text-zinc-400">
                          {sort === "active" ? "Active " : "Created "}
                          <Timestamp relative value={when} />
                        </span>
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

          {membership ? (
            <AccountCIDaemonPanel account={account} canEdit={canAdmin(membership)} />
          ) : null}

          {membership?.kind === "personal" ? <YourAccounts memberships={memberships} /> : null}

          {membership && kind === "organization" ? <AccountPeople account={account} membership={membership} /> : null}
        </>
      )}
    </section>
  );
}

// On your own page: the organizations you belong to, with your role, like a
// GitHub profile's organizations, and the agents you have claimed.
function YourAccounts({ memberships }: { memberships: readonly Membership[] }) {
  const groups = [{ kind: "organization", title: "Organizations", empty: "You do not belong to any organizations yet." }];
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
                      params={{ account: m.account }}
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
      <div className="mt-10">
        <OwnedAgents />
      </div>
      <div className="mt-10">
        <AgentDaemons />
      </div>
    </>
  );
}

const pictureTypes = ["image/png", "image/jpeg", "image/gif", "image/webp"];
const maxPictureBytes = 5 * 1024 * 1024;

// PictureEditor uploads or removes the account's picture. It saves at once,
// apart from the profile form's Save; without an upload the account shows the
// pixel picture generated from its name.
function PictureEditor({ account, kind, version }: { account: string; kind: string; version: string }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [problem, setProblem] = useState("");
  const change = useMutation({
    mutationFn: (image: Uint8Array | undefined) => api.setAccountAvatar({ account, image }),
    onSuccess: (updated) => {
      queryClient.setQueryData(accountProfileKey(account), updated);
      rememberAvatarVersion(account, updated.avatarVersion ?? "");
    }
  });

  async function choose(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    // Choosing the same file again should upload it again.
    event.target.value = "";
    if (!file) {
      return;
    }
    if (!pictureTypes.includes(file.type)) {
      setProblem("Choose a PNG, JPEG, GIF or WebP picture.");
      return;
    }
    if (file.size > maxPictureBytes) {
      setProblem("Choose a picture of at most 5 MB.");
      return;
    }
    setProblem("");
    change.mutate(new Uint8Array(await file.arrayBuffer()));
  }

  return (
    <div className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
      Picture
      <div className="flex flex-wrap items-center gap-3">
        <AccountAvatar account={account} kind={kind} size="lg" version={version} />
        <input accept={pictureTypes.join(",")} aria-label="Picture file" className="hidden" onChange={(e) => void choose(e)} ref={input} type="file" />
        <Button disabled={change.isPending} onClick={() => input.current?.click()} size="md">
          {change.isPending ? "Saving..." : "Upload picture"}
        </Button>
        {version ? (
          <Button disabled={change.isPending} onClick={() => change.mutate(undefined)} size="md">
            Remove picture
          </Button>
        ) : null}
      </div>
      <p className="font-normal text-slate-500 dark:text-zinc-500">
        {version
          ? "Shown cropped to its center square."
          : "Without a picture of its own, the account shows the pixel picture made from its name."}
      </p>
      {problem || change.isError ? (
        <SliceNotice title="Could not change the picture" tone="error">
          {problem || getErrorMessage(change.error)}
        </SliceNotice>
      ) : null}
    </div>
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
      queryClient.setQueryData(accountProfileKey(account), updated);
      onDone();
    }
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    save.mutate();
  }

  return (
    <form aria-label="Edit profile" className="mt-4 grid max-w-xl gap-3" onSubmit={submit}>
      <PictureEditor account={account} kind={profile.kind ?? ""} version={profile.avatarVersion ?? ""} />
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Display name
        <Input maxLength={64} onChange={(e) => setDisplayName(e.target.value)} value={displayName} />
      </label>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Description
        <textarea className={inputClassName({ className: "h-auto py-2" })} maxLength={280} onChange={(e) => setDescription(e.target.value)} rows={3} value={description} />
      </label>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Website
        <Input onChange={(e) => setWebsite(e.target.value)} placeholder="https://" type="url" value={website} />
      </label>
      {save.isError ? (
        <SliceNotice title="Could not save the profile" tone="error">
          {getErrorMessage(save.error)}
        </SliceNotice>
      ) : null}
      <div className="flex gap-2">
        <Button disabled={save.isPending} size="md" type="submit" variant="primary">
          {save.isPending ? "Saving..." : "Save profile"}
        </Button>
        <Button disabled={save.isPending} onClick={onDone} size="md">
          Cancel
        </Button>
      </div>
    </form>
  );
}
