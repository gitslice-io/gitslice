import { useAuth } from "@clerk/tanstack-react-start";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";

import { RpcError } from "../api/client";
import type { ListSlicesResponse } from "../api/types";
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
import { canAdmin, kindLabel, membershipFor } from "../lib/accounts";
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
  const pages = slicesQuery.data?.pages ?? [];
  const slices = pages.flatMap((page: ListSlicesResponse) => page.slices ?? []);
  const kind = membership?.kind || pages[0]?.accountKind || "";
  const notFound = isNotFound(slicesQuery.error);

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
              <h1 className="truncate text-xl font-semibold text-zinc-950 dark:text-zinc-50">{account}</h1>
              <p className="text-sm text-slate-600 dark:text-zinc-400">
                {kind ? kindLabel(kind) : "Account"}
                {membership?.role ? ` · you are ${article(membership.role)} ${membership.role}` : ""}
              </p>
            </div>
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

          {membership && kind === "organization" ? <AccountPeople account={account} membership={membership} /> : null}
        </>
      )}
    </section>
  );
}

function article(role: string) {
  return /^[aeiou]/.test(role) ? "an" : "a";
}
