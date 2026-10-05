import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";

import type { Slice } from "../api/types";
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

const PAGE_SIZE = 100;

// An account's page: its slices that the visitor can open. Anyone sees the
// public ones; the account's own members see every slice.
export function AccountPage() {
  const api = useApi();
  const { isLoaded } = useAuth();
  const { account: viewerAccount } = useSelection();
  const params = useParams({ strict: false }) as { account?: string };
  const account = params.account ?? "";
  const isOwn = Boolean(viewerAccount) && viewerAccount.toLowerCase() === account.toLowerCase();

  const slicesQuery = useQuery({
    // Wait for Clerk, so a member's token is attached and their private slices
    // are included on the first request.
    enabled: Boolean(isLoaded && account),
    queryKey: ["accountSlices", account, viewerAccount],
    queryFn: async () => {
      const slices: Slice[] = [];
      let cursor = "";
      do {
        const page = await api.listSlices({ account, cursor, pageSize: PAGE_SIZE });
        slices.push(...(page.slices ?? []));
        cursor = page.nextCursor ?? "";
      } while (cursor);
      return slices;
    }
  });
  const slices = slicesQuery.data ?? [];

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
      <p className="mb-4 mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
        {isOwn ? "Your slices. Others see only the public ones." : `Public slices owned by ${account}.`}
      </p>
      {slicesQuery.isPending ? (
        <SliceLoadingBlock />
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
