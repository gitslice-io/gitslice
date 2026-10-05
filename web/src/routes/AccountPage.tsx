import { useAuth } from "@clerk/tanstack-react-start";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";

import { RpcError } from "../api/client";
import type { ListSlicesResponse } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import {
  SliceLoadingBlock,
  SliceNotice,
  VisibilityBadge,
  formatPathPreview,
  getErrorMessage
} from "../components/slices/SlicePageParts";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { useSelection } from "../state/selection";

const PAGE_SIZE = 50;

function isDefinite(error: unknown) {
  return error instanceof RpcError && [400, 401, 403, 404].includes(error.status);
}

function isNotFound(error: unknown) {
  return error instanceof RpcError && (error.status === 404 || error.code === 5 || error.code === "5" || error.code === "NotFound" || error.code === "not_found");
}

// An account's page: its slices that the visitor can open. Anyone sees the
// public ones; the account's own members see every slice.
export function AccountPage() {
  const api = useApi();
  const { isLoaded } = useAuth();
  const { accounts: viewerAccounts } = useSelection();
  const params = useParams({ strict: false }) as { account?: string };
  const account = params.account ?? "";
  // Which of the viewer's accounts this is: their personal one first, then the
  // organizations they belong to.
  const membership = viewerAccounts.findIndex((name) => name.toLowerCase() === account.toLowerCase());

  const slicesQuery = useInfiniteQuery({
    // Wait for Clerk, so a member's token is attached and their private slices
    // are included on the first request.
    enabled: Boolean(isLoaded && account),
    getNextPageParam: (last: ListSlicesResponse) => last.nextCursor || undefined,
    initialPageParam: "",
    queryKey: ["accountSlices", account, viewerAccounts.join(" ")],
    queryFn: ({ pageParam }) => api.listSlices({ account, cursor: pageParam, pageSize: PAGE_SIZE }),
    // Asking again does not change a definite answer (no such account, not
    // signed in, not allowed); only failures that may pass are retried.
    retry: (count, error) => !isDefinite(error) && count < 2
  });
  const slices = (slicesQuery.data?.pages ?? []).flatMap((page: ListSlicesResponse) => page.slices ?? []);

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        breadcrumb={<Breadcrumb items={[{ label: "Home", to: "/" }, { label: `@${account}` }]} />}
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-lg">
            {`@${account}`}
          </h1>
        }
      />
      {isNotFound(slicesQuery.error) ? (
        <div className="mt-2" />
      ) : (
        <p className="mb-4 mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          {membership === 0
            ? "Your slices. Others see only the public ones."
            : membership > 0
              ? `Slices of ${account}, which you belong to. Others see only the public ones.`
              : `Public slices owned by ${account}.`}
        </p>
      )}
      {slicesQuery.isPending ? (
        <SliceLoadingBlock />
      ) : isNotFound(slicesQuery.error) ? (
        <SliceNotice title="No such account">There is no account named {account}.</SliceNotice>
      ) : slicesQuery.isError ? (
        <SliceNotice title="Could not load slices" tone="error">
          {getErrorMessage(slicesQuery.error)}
        </SliceNotice>
      ) : slices.length === 0 ? (
        <SliceNotice title="No public slices">{account} has no public slices.</SliceNotice>
      ) : (
        <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/50">
          {slices.map((slice) => {
            const routeParams = toSliceRouteParams(slice.ref);
            const name = slice.ref?.slice ?? slice.id ?? "slice";
            const paths = slice.definition?.includedPaths ?? [];
            return (
              <li key={slice.id ?? name}>
                {routeParams ? (
                  <Link
                    className="flex items-center justify-between gap-3 px-4 py-3 transition hover:bg-slate-50 dark:hover:bg-zinc-950"
                    params={routeParams}
                    to="/slices/$account/$slice"
                  >
                    <SliceRow name={name} paths={paths} />
                    <VisibilityBadge visibility={slice.definition?.visibility} />
                  </Link>
                ) : (
                  <div className="flex items-center justify-between gap-3 px-4 py-3">
                    <SliceRow name={name} paths={paths} />
                    <VisibilityBadge visibility={slice.definition?.visibility} />
                  </div>
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
    </section>
  );
}

function SliceRow({ name, paths }: { name: string; paths: string[] }) {
  return (
    <span className="min-w-0">
      <span className="block truncate font-medium text-zinc-950 dark:text-zinc-50">{name}</span>
      {paths.length > 0 ? (
        <span className="block truncate text-xs text-slate-500 dark:text-zinc-400">{formatPathPreview(paths)}</span>
      ) : null}
    </span>
  );
}
