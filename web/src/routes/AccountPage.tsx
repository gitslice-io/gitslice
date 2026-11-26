import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";

import { isDefiniteClientError, isNotFoundError } from "../api/rpcError";
import type { ListSlicesResponse } from "../api/types";
import { useApi } from "../api/useApi";
import { useAppAuth } from "../auth/AppAuthProvider";
import { rememberAvatarVersion } from "../components/AccountAvatar";
import { Breadcrumb } from "../components/Breadcrumb";
import { AccountCIDaemonPanel } from "../components/slices/AccountCIDaemonPanel";
import { AccountPeople } from "../components/slices/AccountPeople";
import { AgentDaemons } from "../components/slices/AgentDaemons";
import { MonorepoCard } from "../components/slices/MonorepoCard";
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
import { canAdmin, membershipFor } from "../lib/accounts";
import { isHomeSlice, isSliceSort, loadSliceSort, saveSliceSort, sliceSorts, sortSlices, type SliceSort } from "../lib/sliceOrder";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { accountProfileQuery, accountSlicesQuery } from "../queries/accounts";
import { useSelection } from "../state/selection";
import { AccountProfileHeader } from "./account/AccountProfileHeader";
import { ProfileEditor } from "./account/ProfileEditor";
import { YourAccounts } from "./account/YourAccounts";
import type { AccountSearch, AccountTab } from "./searchSchemas";

// An account's page, personal or organization: its slices that the visitor can
// open, and for account members, tabbed sections for agents, CI runners, and
// organization people. Anyone sees the public slices; members see every slice.
export function AccountPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { authKind, isLoaded } = useAppAuth();
  const { memberships, activeAccount, setActiveAccount } = useSelection();
  const params = useParams({ from: "/publicApp/accounts/$account" });
  const search = useSearch({ strict: false }) as AccountSearch;
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
  const kind = membership?.kind || profile?.kind || pages[0]?.accountKind || "";
  const displayKind = authKind === "agent" && membership?.kind === "personal" ? "agent" : kind;
  const notFound = isNotFoundError(slicesQuery.error) || isNotFoundError(profileQuery.error);
  // An organization's owners and admins edit its profile; a person edits their own.
  const canEditProfile =
    membership?.kind === "personal" || (membership?.kind === "organization" && canAdmin(membership));
  const [editing, setEditing] = useState(false);

  const tabs: Array<{ id: AccountTab; label: string }> = [];
  if (membership) {
    tabs.push({ id: "overview", label: "Overview" });
    if (membership.kind === "personal") {
      tabs.push({ id: "agents", label: "Agents" });
    }
    if (membership.kind === "personal" || canAdmin(membership)) {
      tabs.push({ id: "runners", label: "CI Runners" });
    }
    if (kind === "organization") {
      tabs.push({ id: "people", label: "People" });
    }
  }
  const hasTabs = tabs.length > 1;
  const requestedTab: AccountTab =
    search.tab && tabs.some((tab) => tab.id === search.tab) ? search.tab : "overview";
  const [activeTab, setActiveTab] = useState<AccountTab>(requestedTab);
  useEffect(() => {
    setActiveTab(requestedTab);
  }, [requestedTab, account]);

  const resolvedTab: AccountTab = tabs.some((tab) => tab.id === activeTab) ? activeTab : "overview";
  const tabRefs = useRef<Partial<Record<AccountTab, HTMLButtonElement | null>>>({});

  function selectTab(next: AccountTab) {
    setActiveTab(next);
    const button = tabRefs.current[next];
    if (button && typeof button.scrollIntoView === "function") {
      button.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
    void navigate({
      params: { account },
      replace: true,
      search: next === "overview" ? {} : { tab: next },
      to: "/accounts/$account"
    });
  }

  function handleTabKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    let nextIndex = -1;
    if (event.key === "ArrowRight") {
      nextIndex = (index + 1) % tabs.length;
    } else if (event.key === "ArrowLeft") {
      nextIndex = (index - 1 + tabs.length) % tabs.length;
    } else if (event.key === "Home") {
      nextIndex = 0;
    } else if (event.key === "End") {
      nextIndex = tabs.length - 1;
    }
    if (nextIndex === -1) {
      return;
    }
    event.preventDefault();
    const target = tabRefs.current[tabs[nextIndex].id];
    target?.focus();
    if (target && typeof target.scrollIntoView === "function") {
      target.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
  }

  const overviewContent = (
    <>
      {homeSlice ? (
        <MonorepoCard account={account} className={hasTabs ? "" : "mt-8"} homeSlice={homeSlice} />
      ) : null}

      <div className={`mb-2 ${homeSlice || !hasTabs ? "mt-8" : ""} flex items-center justify-between gap-3`}>
        <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          {homeSlice ? "Slices (Views)" : "Slices"}
        </h2>
        <label className="flex shrink-0 items-center gap-1.5 text-xs text-slate-500 dark:text-zinc-400">
          Sort
          <select
            className="h-8 rounded-md border border-slate-300 bg-white px-2 text-xs font-medium text-zinc-950 outline-none focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:focus:ring-zinc-700"
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
        {membership?.kind === "personal" ? (
          <>
            Focused views, checkouts, access boundaries, and Git mirrors over{" "}
            <span className="whitespace-nowrap font-mono">/{account}</span>. Others see only the public ones.
          </>
        ) : membership ? (
          <>
            Slices of {account}, which you belong to — focused views, access boundaries, and Git mirrors over{" "}
            <span className="whitespace-nowrap font-mono">/{account}</span>. Others see only the public ones.
          </>
        ) : (
          `Public slices owned by ${account}.`
        )}
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
          Browse everything in <span className="whitespace-nowrap font-mono">/{account}</span> above, or create a slice view to scope checkouts,
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
          <Button
            disabled={slicesQuery.isFetchingNextPage}
            onClick={() => void slicesQuery.fetchNextPage()}
            size="md"
            variant="secondary"
          >
            {slicesQuery.isFetchingNextPage ? "Loading…" : "Show more"}
          </Button>
        </div>
      ) : null}

      {membership?.kind === "personal" ? <YourAccounts memberships={memberships} /> : null}
    </>
  );

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <Breadcrumb items={[{ label: "Home", to: "/" }, { label: `@${account}` }]} />
      {notFound ? (
        <div className="mt-4">
          <SliceNotice title="No such account">There is no account named {account}.</SliceNotice>
        </div>
      ) : (
        <>
          <AccountProfileHeader
            account={account}
            activeAccount={activeAccount}
            canEditProfile={canEditProfile}
            displayKind={displayKind}
            editing={editing}
            membership={membership}
            onEdit={() => setEditing(true)}
            onSwitchAccount={setActiveAccount}
            profile={profile}
          />

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

          {hasTabs ? (
            <>
              <div
                aria-label="Account sections"
                className="mt-6 flex flex-nowrap gap-1 overflow-x-auto border-b border-slate-200 dark:border-zinc-800"
                role="tablist"
              >
                {tabs.map((tab, index) => {
                  const selected = resolvedTab === tab.id;
                  return (
                    <button
                      aria-selected={selected}
                      className="relative flex-none cursor-pointer px-3 py-2.5 text-sm font-medium text-slate-600 transition hover:text-zinc-950 focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-zinc-950 aria-selected:text-zinc-950 after:pointer-events-none after:absolute after:inset-x-0 after:bottom-0 after:border-b-2 after:border-transparent aria-selected:after:border-zinc-950 dark:text-zinc-400 dark:hover:text-zinc-50 dark:focus-visible:outline-zinc-100 dark:aria-selected:text-zinc-50 dark:aria-selected:after:border-zinc-50"
                      id={`account-tab-${tab.id}`}
                      key={tab.id}
                      onClick={() => selectTab(tab.id)}
                      onKeyDown={(event) => handleTabKeyDown(event, index)}
                      ref={(el) => {
                        tabRefs.current[tab.id] = el;
                      }}
                      role="tab"
                      tabIndex={selected ? 0 : -1}
                      type="button"
                    >
                      {tab.label}
                    </button>
                  );
                })}
              </div>

              <div
                aria-labelledby={`account-tab-${resolvedTab}`}
                className="mt-6"
                role="tabpanel"
              >
                {resolvedTab === "overview" ? overviewContent : null}
                {resolvedTab === "agents" && membership?.kind === "personal" ? <OwnedAgents /> : null}
                {resolvedTab === "runners" && membership ? (
                  <div className="grid gap-8">
                    <AccountCIDaemonPanel
                      account={account}
                      canEdit={canAdmin(membership)}
                      className=""
                    />
                    {membership.kind === "personal" ? <AgentDaemons /> : null}
                  </div>
                ) : null}
                {resolvedTab === "people" && membership && kind === "organization" ? (
                  <AccountPeople account={account} className="" membership={membership} />
                ) : null}
              </div>
            </>
          ) : (
            overviewContent
          )}
        </>
      )}
    </section>
  );
}
