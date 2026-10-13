import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { useApi } from "../../api/useApi";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
import { VisibilityBadge, formatPathPreview } from "./SlicePageParts";

/**
 * Slices other accounts shared with the signed-in person or agent
 * (design/26_slice_members.md), with their role on each. Nothing shows when
 * none are shared or the server does not support it.
 */
export function SharedSlices() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const sharedQuery = useQuery({
    enabled: Boolean(isLoaded && isSignedIn),
    queryKey: ["sharedSlices"],
    queryFn: async () => (await api.listSharedSlices({})).slices ?? [],
    retry: false
  });
  const shared = (sharedQuery.data ?? []).filter((item) => item.slice?.ref);
  if (shared.length === 0) {
    return null;
  }
  return (
    <section>
      <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Shared with you</h2>
      <p className="text-sm leading-6 text-slate-600 dark:text-zinc-400">
        Slices in other accounts that their owners shared with you.
      </p>
      <ul className="mt-4 divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900">
        {shared.map(({ slice, role }) => {
          const ref = slice!.ref!;
          const params = toSliceRouteParams(ref);
          return (
            <li className="flex flex-wrap items-center gap-3 px-4 py-3" key={`${ref.account}/${ref.slice}`}>
              {params ? (
                <Link
                  className="min-w-0 flex-1 truncate font-mono text-sm font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                  params={params as never}
                  to="/slices/$account/$slice"
                >
                  {ref.account}/{ref.slice}
                </Link>
              ) : null}
              <span className="font-mono text-xs text-slate-500 dark:text-zinc-400">
                {formatPathPreview(slice?.definition?.includedPaths ?? [])}
              </span>
              <VisibilityBadge visibility={slice?.definition?.visibility} />
              <span className="rounded bg-slate-100 px-1.5 py-0.5 text-xs text-slate-700 dark:bg-zinc-800 dark:text-zinc-300">
                {role}
              </span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
