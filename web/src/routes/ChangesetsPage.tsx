import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo } from "react";

import { useApi } from "../api/useApi";
import { useAppAuth } from "../auth/AppAuthProvider";
import { Breadcrumb, type Crumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import {
  SliceLoadingBlock,
  SliceNotice,
  getErrorMessage
} from "../components/slices/SlicePageParts";
import { Button } from "../components/ui/Button";
import { cn } from "../lib/cn";
import { sliceBreadcrumbItems } from "../lib/sliceRoutes";
import { changesetsListQuery } from "../queries/changesets";
import { useSelection } from "../state/selection";
import {
  ChangesetsTable,
  type ChangesetsQueryKey
} from "./changesets-list/ChangesetsTable";
import {
  ChangesetsToolbar,
  MissingSliceState
} from "./changesets-list/ChangesetsToolbar";
import { parseStatusTab, type StatusTab } from "../lib/changesetFilters";
import { parseSliceSearch } from "./stackPageUtils";

export function ChangesetsPage() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAppAuth();
  const { accounts: viewerAccounts } = useSelection();
  const canManage = Boolean(isLoaded && isSignedIn);
  const navigate = useNavigate();
  // Validated by the route (searchSchemas): the slice, the status group (open
  // when absent) and the typed text.
  const search = useSearch({ from: "/publicApp/changesets" });
  const sliceRef = parseSliceSearch(search.slice);
  const account = sliceRef?.account ?? "";
  const slice = sliceRef?.slice ?? "";
  // Which changesets to list lives in the URL, so a view can be shared.
  const tab = parseStatusTab(search.status);
  const typed = search.q ?? "";
  const queryKey = useMemo<ChangesetsQueryKey>(
    () => ["changesets", account, slice],
    [account, slice]
  );

  const changesetsQuery = useInfiniteQuery({
    ...changesetsListQuery(api, { account, slice }, tab, typed),
    // Wait for Clerk before fetching so a signed-in user's token is attached on
    // the first request (this public page renders before auth resolves). See
    // ChangesetDetailPage for the same reasoning.
    enabled: Boolean(isLoaded && account && slice),
    // Keep the list on screen, dimmed, while another view of it loads.
    placeholderData: keepPreviousData
  });

  // The server's order: most recently updated first, page after page.
  const changesets = useMemo(
    () => changesetsQuery.data?.pages.flatMap((page) => page.changesets ?? []) ?? [],
    [changesetsQuery.data?.pages]
  );
  const filtered = tab !== "open" || typed.trim() !== "";
  const queryError = changesetsQuery.error;
  const loadMoreFailed = changesetsQuery.isFetchNextPageError;

  // A tab is a place to go back to, so choosing one adds a history entry;
  // typing only refines the one you are on.
  const setView = (next: { status?: StatusTab; q?: string }, replace: boolean) =>
    void navigate({
      replace,
      search: (prev) => {
        const status = next.status ?? tab;
        const q = next.q ?? typed;
        return {
          ...prev,
          // Open and no text are the defaults, and keep the address clean.
          status: status === "open" ? undefined : status,
          q: q.trim() === "" ? undefined : q
        };
      },
      to: "/changesets"
    });

  const breadcrumbItems: Crumb[] = [{ label: "Home", to: "/" }];
  if (sliceRef) {
    breadcrumbItems.push(...sliceBreadcrumbItems(sliceRef, viewerAccounts));
  }
  breadcrumbItems.push({ label: "Changesets" });
  const pageTitle = sliceRef ? `${account}:${slice} · Changesets` : "Changesets";
  const description = sliceRef
    ? "Review and merge changesets authored against this slice."
    : "Open a slice and use its Changesets tab to see the slice-scoped review queue.";

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        breadcrumb={<Breadcrumb items={breadcrumbItems} />}
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-lg">
            {pageTitle}
          </h1>
        }
      />
      <p className="mb-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">{description}</p>
      <div className="mt-0">
        {!sliceRef ? (
          <MissingSliceState
            navigateToChangeset={(id) => {
              void navigate({ params: { id }, to: "/cs/$id" });
            }}
          />
        ) : (
          <>
            <ChangesetsToolbar
              onSearch={(q) => setView({ q }, true)}
              onTab={(status) => setView({ status }, false)}
              tab={tab}
              typed={typed}
            />
            {!isLoaded || changesetsQuery.isPending ? (
              <SliceLoadingBlock />
            ) : changesetsQuery.isError && !changesetsQuery.data ? (
              <SliceNotice title="Could not load changesets" tone="error">
                {getErrorMessage(queryError)}
              </SliceNotice>
            ) : changesets.length === 0 ? (
              <SliceNotice title={filtered ? "No changesets match." : "No changesets for this slice yet."}>
                {filtered ? (
                  <>
                    Nothing in {tab === "all" ? "any status" : `the ${tab} changesets`}
                    {typed.trim() ? ` matches "${typed.trim()}"` : ""}.{" "}
                    <button
                      className="font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                      onClick={() => setView({ status: "all", q: "" }, false)}
                      type="button"
                    >
                      Show all changesets
                    </button>
                  </>
                ) : (
                  "New changesets created from the slice workspace will appear here."
                )}
              </SliceNotice>
            ) : (
              <div className={cn("transition-opacity", changesetsQuery.isPlaceholderData && "opacity-60")}>
                <ChangesetsTable
                  api={api}
                  canManage={canManage}
                  changesets={changesets}
                  queryKey={queryKey}
                />
                <div className="mt-3 flex items-center justify-between gap-3 text-sm text-slate-500 dark:text-zinc-400">
                  <span>
                    {changesets.length} {changesets.length === 1 ? "changeset" : "changesets"}
                    {changesetsQuery.hasNextPage ? " so far" : ""}
                  </span>
                  {changesetsQuery.hasNextPage ? (
                    <Button
                      disabled={changesetsQuery.isFetchingNextPage}
                      onClick={() => void changesetsQuery.fetchNextPage()}
                      size="md"
                    >
                      {changesetsQuery.isFetchingNextPage ? "Loading…" : "Show more"}
                    </Button>
                  ) : null}
                </div>
                {loadMoreFailed ? (
                  <div className="mt-3">
                    <SliceNotice title="Could not load more changesets" tone="error">
                      {getErrorMessage(queryError)}
                    </SliceNotice>
                  </div>
                ) : null}
              </div>
            )}
          </>
        )}
      </div>
    </section>
  );
}
