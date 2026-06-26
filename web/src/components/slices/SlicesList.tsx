import { useQuery } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";

import type { Slice } from "../../api/types";
import { useApi } from "../../api/useApi";
import { shortHash } from "../../lib/objectId";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
import { useSelection } from "../../state/selection";
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
  const effectiveAccount = (search.account || selection.account || "").trim();

  const slicesQuery = useQuery({
    enabled: effectiveAccount.length > 0,
    queryKey: ["slices", effectiveAccount],
    queryFn: async () => {
      const slices: Slice[] = [];
      let cursor = "";

      do {
        const response = await api.listSlices({
          account: effectiveAccount,
          cursor,
          pageSize: PAGE_SIZE
        });

        slices.push(...(response.slices ?? []));
        cursor = response.nextCursor ?? "";
      } while (cursor);

      return slices;
    }
  });
  const slices = slicesQuery.data ?? [];

  return (
    <section>
      <div className="flex items-end justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-zinc-950">Slices</h2>
          <p className="text-sm leading-6 text-slate-600">
            Definitions for slices under the selected account.
          </p>
        </div>
        <Link
          className="shrink-0 rounded-md border border-slate-200 bg-white px-2.5 py-1 text-xs font-medium text-slate-700 transition hover:border-slate-300 hover:bg-slate-50 hover:text-zinc-950 active:scale-[0.98]"
          to="/slices/new"
        >
          New slice
        </Link>
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
        ) : (
          <>
            {/* Mobile: a compact stacked list — a 5-column table collapses to a
                sparse two-column layout on narrow screens, so render rows. */}
            <ul className="divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 md:hidden">
              {slices.map((slice) => {
                const sliceId = slice.id ?? "";
                const routeParams = toSliceRouteParams(slice.ref);
                const name = sliceDisplayName(slice);

                return (
                  <li key={sliceId || name}>
                    {routeParams ? (
                      <Link
                        className="flex items-center justify-between gap-3 px-4 py-3 transition hover:bg-slate-50"
                        params={routeParams}
                        to="/slices/$account/$slice"
                      >
                        <span className="min-w-0 truncate font-medium text-zinc-950">
                          {name}
                        </span>
                        <VisibilityBadge
                          visibility={slice.definition?.visibility}
                        />
                      </Link>
                    ) : (
                      <div className="flex items-center justify-between gap-3 px-4 py-3">
                        <span className="min-w-0 truncate font-medium text-zinc-950">
                          {name}
                        </span>
                        <VisibilityBadge
                          visibility={slice.definition?.visibility}
                        />
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>

            {/* Desktop: the full detail table. */}
            <div className="hidden overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 md:block">
              <div className="overflow-x-auto">
                <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
                  <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
                    <tr>
                      <th className="px-4 py-3">Slice</th>
                      <th className="px-4 py-3">Visibility</th>
                      <th className="px-4 py-3">Version</th>
                      <th className="px-4 py-3">Definition hash</th>
                      <th className="px-4 py-3">Included paths</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-200">
                    {slices.map((slice) => {
                      const paths = slice.definition?.includedPaths ?? [];
                      const sliceId = slice.id ?? "";
                      const routeParams = toSliceRouteParams(slice.ref);

                      return (
                        <tr
                          className="align-top transition hover:bg-slate-50"
                          key={sliceId || sliceDisplayName(slice)}
                        >
                          <td className="px-4 py-3 font-medium text-zinc-950">
                            {routeParams ? (
                              <Link
                                className="break-words underline decoration-slate-300 underline-offset-4 hover:decoration-slate-700"
                                params={routeParams}
                                to="/slices/$account/$slice"
                              >
                                {sliceDisplayName(slice)}
                              </Link>
                            ) : (
                              sliceDisplayName(slice)
                            )}
                          </td>
                          <td className="px-4 py-3">
                            <VisibilityBadge
                              visibility={slice.definition?.visibility}
                            />
                          </td>
                          <td className="px-4 py-3 font-mono text-xs text-slate-700">
                            {slice.definition?.version ?? "unknown"}
                          </td>
                          <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-slate-700">
                            {slice.definitionHash ? (
                              <span title={slice.definitionHash}>
                                {shortHash(slice.definitionHash)}
                              </span>
                            ) : (
                              "none"
                            )}
                          </td>
                          <td className="px-4 py-3 text-slate-700">
                            <span className="font-medium text-zinc-950">
                              {paths.length}
                            </span>{" "}
                            <span>{formatPathPreview(paths)}</span>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          </>
        )}
      </div>
    </section>
  );
}
