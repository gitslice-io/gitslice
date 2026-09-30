import { useQueries } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";

import { accountSlicesQuery } from "../../api/queries";
import type { Slice } from "../../api/types";
import { useApi } from "../../api/useApi";
import { shortHash } from "../../lib/objectId";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
import { useSelection } from "../../state/selection";
import { useOwnedAgents } from "./OwnedAgents";
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

export function SlicesList() {
  const api = useApi();
  const selection = useSelection();
  const search = useSearch({ strict: false }) as SlicesSearch;
  const explicitAccount = (search.account || "").trim();
  const effectiveAccount = (explicitAccount || selection.account || "").trim();
  const ownedAgents = useOwnedAgents();
  // Without an explicit ?account=, the home list also covers the accounts of
  // agents you co-own, so their slices sit next to yours.
  const agentAccounts = explicitAccount
    ? []
    : Array.from(
        new Set(
          ownedAgents.agents
            .map((agent) => agent.account ?? "")
            .filter((account) => account && account !== effectiveAccount)
        )
      );
  const accounts = effectiveAccount ? [effectiveAccount, ...agentAccounts] : [];
  const agentAccountSet = new Set(agentAccounts);

  const slicesQueries = useQueries({
    queries: accounts.map((account) => ({
      ...accountSlicesQuery(api, account),
      enabled: Boolean(account)
    }))
  });
  const ownSlicesQuery = slicesQueries[0];
  const agentSlicesQueries = slicesQueries.slice(1);
  const slices = deduplicateSlices(
    slicesQueries.flatMap((query) => query.data ?? [])
  );
  const isLoadingAgentSlices =
    !explicitAccount &&
    (ownedAgents.isLoading || agentSlicesQueries.some((query) => query.isPending));
  const agentSliceErrors = agentAccounts.filter(
    (_, index) => agentSlicesQueries[index]?.isError
  );

  return (
    <section>
      <div className="flex items-end justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Slices</h2>
          <p className="text-sm leading-6 text-slate-600 dark:text-zinc-400">
            {agentAccounts.length > 0
              ? "Slices in your account and in your agents' accounts."
              : "Definitions for slices under the selected account."}
          </p>
        </div>
        <Link
          className="shrink-0 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2.5 py-1 text-xs font-medium text-slate-700 dark:text-zinc-300 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50 active:scale-[0.98]"
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
        ) : ownSlicesQuery?.isPending ? (
          <SliceLoadingBlock />
        ) : ownSlicesQuery?.isError ? (
          <SliceNotice title="Could not load slices" tone="error">
            {getErrorMessage(ownSlicesQuery.error)}
          </SliceNotice>
        ) : slices.length === 0 ? (
          // Nothing of your own yet: wait for your agents' slices before
          // concluding the list is empty, rather than flashing an empty table.
          isLoadingAgentSlices ? (
            <SliceLoadingBlock />
          ) : (
            <SliceNotice title="No slices returned">
              The server did not return any slices for this account.
            </SliceNotice>
          )
        ) : (
          <>
            {/* Mobile: a compact stacked list — a 5-column table collapses to a
                sparse two-column layout on narrow screens, so render rows. */}
            <ul className="divide-y divide-slate-200 dark:divide-zinc-800 overflow-hidden rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-sm shadow-slate-200/50 dark:shadow-black/50 md:hidden">
              {slices.map((slice) => {
                const sliceId = slice.id ?? "";
                const routeParams = toSliceRouteParams(slice.ref);
                const name = sliceDisplayName(slice);

                return (
                  <li key={sliceId || name}>
                    {routeParams ? (
                      <Link
                        className="flex items-center justify-between gap-3 px-4 py-3 transition hover:bg-slate-50 dark:hover:bg-zinc-950"
                        params={routeParams}
                        to="/slices/$account/$slice"
                      >
                        <span className="min-w-0 truncate font-medium text-zinc-950 dark:text-zinc-50">
                          {name}
                          {agentAccountSet.has(slice.ref?.account ?? "") ? (
                            <AgentBadge />
                          ) : null}
                        </span>
                        <VisibilityBadge
                          visibility={slice.definition?.visibility}
                        />
                      </Link>
                    ) : (
                      <div className="flex items-center justify-between gap-3 px-4 py-3">
                        <span className="min-w-0 truncate font-medium text-zinc-950 dark:text-zinc-50">
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
            <div className="hidden overflow-hidden rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-sm shadow-slate-200/50 dark:shadow-black/50 md:block">
              <div className="overflow-x-auto">
                <table className="min-w-full divide-y divide-slate-200 dark:divide-zinc-800 text-left text-sm">
                  <thead className="bg-slate-50 dark:bg-zinc-950 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
                    <tr>
                      <th className="px-4 py-3">Slice</th>
                      <th className="px-4 py-3">Visibility</th>
                      <th className="px-4 py-3">Version</th>
                      <th className="px-4 py-3">Definition hash</th>
                      <th className="px-4 py-3">Included paths</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
                    {slices.map((slice) => {
                      const paths = slice.definition?.includedPaths ?? [];
                      const sliceId = slice.id ?? "";
                      const routeParams = toSliceRouteParams(slice.ref);

                      return (
                        <tr
                          className="align-top transition hover:bg-slate-50 dark:hover:bg-zinc-950"
                          key={sliceId || sliceDisplayName(slice)}
                        >
                          <td className="px-4 py-3 font-medium text-zinc-950 dark:text-zinc-50">
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
                            {agentAccountSet.has(slice.ref?.account ?? "") ? (
                              <AgentBadge />
                            ) : null}
                          </td>
                          <td className="px-4 py-3">
                            <VisibilityBadge
                              visibility={slice.definition?.visibility}
                            />
                          </td>
                          <td className="px-4 py-3 font-mono text-xs text-slate-700 dark:text-zinc-300">
                            {slice.definition?.version ?? "unknown"}
                          </td>
                          <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-slate-700 dark:text-zinc-300">
                            {slice.definitionHash ? (
                              <span title={slice.definitionHash}>
                                {shortHash(slice.definitionHash)}
                              </span>
                            ) : (
                              "none"
                            )}
                          </td>
                          <td className="px-4 py-3 text-slate-700 dark:text-zinc-300">
                            <span className="font-medium text-zinc-950 dark:text-zinc-50">
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
            {isLoadingAgentSlices ? (
              <p className="mt-3 text-xs text-slate-500 dark:text-zinc-400">
                Loading your agents&apos; slices…
              </p>
            ) : null}
            {agentSliceErrors.map((account) => (
              <p
                className="mt-3 text-xs text-slate-500 dark:text-zinc-400"
                key={account}
              >
                Could not load slices for {account}.
              </p>
            ))}
          </>
        )}
      </div>
    </section>
  );
}

function deduplicateSlices(slices: Slice[]) {
  const seen = new Set<string>();

  return slices.filter((slice) => {
    const key =
      slice.id ??
      `${slice.ref?.account ?? ""}:${slice.ref?.slice ?? ""}:${sliceDisplayName(slice)}`;
    if (seen.has(key)) {
      return false;
    }

    seen.add(key);
    return true;
  });
}

function AgentBadge() {
  return (
    <span className="ml-2 rounded-full border border-sky-200 bg-sky-50 px-1.5 py-0.5 align-middle text-[10px] font-medium text-sky-800 dark:border-sky-900/60 dark:bg-sky-950/40 dark:text-sky-300">
      agent
    </span>
  );
}
