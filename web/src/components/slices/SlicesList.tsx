import { useQuery } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";

import type { Slice } from "../../api/types";
import { useApi } from "../../api/useApi";
import { isHomeSlice } from "../../lib/sliceOrder";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
import { canAdmin, kindLabel, membershipFor } from "../../lib/accounts";
import { slicesListKey } from "../../queries/slices";
import { useSelection } from "../../state/selection";
import { MonorepoCard } from "./MonorepoCard";
import {
  SliceLoadingBlock,
  SliceNotice,
  VisibilityBadge,
  formatPathPreview,
  getErrorMessage,
  sliceDisplayName
} from "./SlicePageParts";

interface SlicesSearch {
  account?: string;
}

const PAGE_SIZE = 100;

export function SlicesList() {
  const api = useApi();
  const selection = useSelection();
  const search = useSearch({ strict: false }) as SlicesSearch;
  const explicitAccount = (search.account || "").trim();
  // The account picked in the top bar's switcher, or ?account= to look at one.
  const effectiveAccount = (explicitAccount || selection.activeAccount || selection.account || "").trim();
  const activeMembership = membershipFor(selection.memberships, effectiveAccount);
  const isPersonalContext = !explicitAccount && (activeMembership?.kind ?? "personal") === "personal";
  // Only the active account's slices: agents and organizations are a switch
  // away in the top bar, each with its own list.
  const slicesQuery = useQuery({
    enabled: Boolean(effectiveAccount),
    queryKey: slicesListKey(effectiveAccount),
    queryFn: () => listAllSlices(api, effectiveAccount)
  });
  const slices = slicesQuery.data ?? [];
  const homeSlice = slices.find((slice) => isHomeSlice(slice));
  const viewSlices = homeSlice ? slices.filter((slice) => slice !== homeSlice) : slices;

  return (
    <div className="grid gap-8">
      {homeSlice && effectiveAccount ? (
        <MonorepoCard account={effectiveAccount} homeSlice={homeSlice} />
      ) : null}

      <section>
        <div className="flex items-end justify-between gap-3">
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
              {isPersonalContext
                ? homeSlice
                  ? "Slices (Views)"
                  : "Slices"
                : `Slices in ${effectiveAccount}`}
            </h2>
            <p className="text-sm leading-6 text-slate-600 dark:text-zinc-400">
              {isPersonalContext
                ? "Slices in your account. Switch to an agent or organization from the top bar to see its slices."
                : `Slices of the ${kindLabel(activeMembership?.kind ?? "").toLowerCase()} ${effectiveAccount}. Switch accounts from the top bar.`}
            </p>
          </div>
          {canAdmin(activeMembership) ? (
            <Link
              className="shrink-0 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2.5 py-1 text-xs font-medium text-slate-700 dark:text-zinc-300 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50 active:scale-[0.98]"
              search={{ account: effectiveAccount }}
              to="/slices/new"
            >
              New slice
            </Link>
          ) : null}
        </div>

        <div className="mt-4">
          {selection.isLoading ? (
            <SliceLoadingBlock />
          ) : selection.error ? (
            <SliceNotice title="Could not load your home account" tone="error">
              {getErrorMessage(selection.error)}
            </SliceNotice>
          ) : !effectiveAccount ? (
            <SliceNotice title="Select an account">
              Your signed-in session did not return a home account.
            </SliceNotice>
          ) : slicesQuery.isLoading ? (
            <SliceLoadingBlock />
          ) : slicesQuery.isError ? (
            <SliceNotice title="Could not load slices" tone="error">
              {getErrorMessage(slicesQuery.error)}
            </SliceNotice>
          ) : slices.length === 0 ? (
            <SliceNotice title="No slices returned">
              The server did not return any slices for this account.
            </SliceNotice>
          ) : viewSlices.length === 0 ? (
            <SliceNotice title="No additional slice views yet">
              Browse everything in <span className="whitespace-nowrap font-mono">/{effectiveAccount}</span> above, or create a slice view to scope
              checkouts, share specific folders, or mirror a folder to Git.
            </SliceNotice>
          ) : (
            <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/50">
              {viewSlices.map((slice) => {
                const sliceId = slice.id ?? "";
                const routeParams = toSliceRouteParams(slice.ref);
                const name = formatHomeSliceName(slice);
                const paths = slice.definition?.includedPaths ?? [];
                const body = (
                  <>
                    <span className="min-w-0">
                      <span className="block truncate text-sm font-medium text-zinc-950 dark:text-zinc-50">
                        {name}
                      </span>
                      {paths.length > 0 ? (
                        <span className="mt-0.5 block truncate font-mono text-xs text-slate-500 dark:text-zinc-400">
                          {formatPathPreview(paths)}
                        </span>
                      ) : null}
                    </span>
                    <VisibilityBadge visibility={slice.definition?.visibility} />
                  </>
                );

                return (
                  <li key={sliceId || name}>
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
        </div>
      </section>
    </div>
  );
}

async function listAllSlices(api: ReturnType<typeof useApi>, account: string) {
  const slices: Slice[] = [];
  let cursor = "";

  do {
    const response = await api.listSlices({
      account,
      cursor,
      pageSize: PAGE_SIZE
    });

    slices.push(...(response.slices ?? []));
    cursor = response.nextCursor ?? "";
  } while (cursor);

  return slices;
}

function formatHomeSliceName(slice: Slice) {
  const account = slice.ref?.account;
  const slug = slice.ref?.slice;
  if (account && slug) {
    return `${account}/${slug}`;
  }
  return sliceDisplayName(slice);
}
