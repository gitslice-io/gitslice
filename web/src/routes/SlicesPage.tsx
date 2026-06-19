import { Link, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import type { Slice } from "../api/types";
import { useApi } from "../api/useApi";
import { shortHash } from "../lib/objectId";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePageHeader,
  formatPathPreview,
  getErrorMessage,
  sliceDisplayName
} from "../components/slices/SlicePageParts";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { useSelection } from "../state/selection";

interface SlicesSearch {
  account?: string;
}

const PAGE_SIZE = 100;

export function SlicesPage() {
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
    <section className="mx-auto w-full max-w-[100rem]">
      <SlicePageHeader
        actions={
          <Link
            className="rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
            to="/slices/new"
          >
            New slice
          </Link>
        }
        title={effectiveAccount ? `Slices for ${effectiveAccount}` : "Slices"}
        description="Definitions for slices under the selected account."
      />

      <div className="mt-8">
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
          <div className="overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
            <div className="overflow-x-auto">
              <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
                <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
                  <tr>
                    <th className="px-3 py-3 sm:px-4">Slice</th>
                    <th className="px-3 py-3 sm:px-4">Visibility</th>
                    <th className="hidden px-4 py-3 md:table-cell">Version</th>
                    <th className="hidden px-4 py-3 md:table-cell">
                      Definition hash
                    </th>
                    <th className="hidden px-4 py-3 md:table-cell">
                      Included paths
                    </th>
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
                        <td className="max-w-[12rem] px-3 py-3 font-medium text-zinc-950 sm:max-w-none sm:px-4">
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
                        <td className="px-3 py-3 text-slate-700 sm:px-4">
                          {slice.definition?.visibility || "unspecified"}
                        </td>
                        <td className="hidden px-4 py-3 font-mono text-xs text-slate-700 md:table-cell">
                          {slice.definition?.version ?? "unknown"}
                        </td>
                        <td className="hidden whitespace-nowrap px-4 py-3 font-mono text-xs text-slate-700 md:table-cell">
                          {slice.definitionHash ? (
                            <span title={slice.definitionHash}>
                              {shortHash(slice.definitionHash)}
                            </span>
                          ) : (
                            "none"
                          )}
                        </td>
                        <td className="hidden px-4 py-3 text-slate-700 md:table-cell">
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
        )}
      </div>
    </section>
  );
}
