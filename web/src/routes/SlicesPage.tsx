import { Link, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import type { Slice } from "../api/types";
import { useApi } from "../api/useApi";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePageHeader,
  formatPathPreview,
  getErrorMessage,
  sliceDisplayName
} from "../components/slices/SlicePageParts";
import { useSelection } from "../state/selection";

interface SlicesSearch {
  account?: string;
}

const PAGE_SIZE = 100;

export function SlicesPage() {
  const api = useApi();
  const { account } = useSelection();
  const search = useSearch({ strict: false }) as SlicesSearch;
  const effectiveAccount = (account || search.account || "").trim();

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
    <section className="mx-auto w-full max-w-7xl">
      <SlicePageHeader
        title={effectiveAccount ? `Slices for ${effectiveAccount}` : "Slices"}
        description="Definitions for slices under the selected account."
      />

      <div className="mt-8">
        {!effectiveAccount ? (
          <SliceNotice title="Select an account">
            Enter an account in the top bar before listing slices.
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

                    return (
                      <tr
                        className="align-top transition hover:bg-slate-50"
                        key={sliceId || sliceDisplayName(slice)}
                      >
                        <td className="px-4 py-3 font-medium text-zinc-950">
                          {sliceId ? (
                            <Link
                              className="underline decoration-slate-300 underline-offset-4 hover:decoration-slate-700"
                              params={{ id: sliceId }}
                              to="/slices/$id"
                            >
                              {sliceDisplayName(slice)}
                            </Link>
                          ) : (
                            sliceDisplayName(slice)
                          )}
                        </td>
                        <td className="px-4 py-3 text-slate-700">
                          {slice.definition?.visibility || "unspecified"}
                        </td>
                        <td className="px-4 py-3 font-mono text-xs text-slate-700">
                          {slice.definition?.version ?? "unknown"}
                        </td>
                        <td className="max-w-xs break-all px-4 py-3 font-mono text-xs text-slate-700">
                          {slice.definitionHash || "none"}
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
        )}
      </div>
    </section>
  );
}
