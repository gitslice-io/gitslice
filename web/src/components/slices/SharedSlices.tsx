import { Link } from "@tanstack/react-router";

import { useSharedSlices } from "../../lib/permissions";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
import { VisibilityBadge, formatPathPreview } from "./SlicePageParts";

/**
 * Slices other accounts shared with the signed-in person or agent
 * (design/26_slice_members.md), with their role on each. Nothing shows when
 * none are shared or the server does not support it.
 */
export function SharedSlices() {
  const sharedQuery = useSharedSlices();
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
      <ul className="mt-4 divide-y divide-slate-200 overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50 dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/50">
        {shared.map(({ slice, role }) => {
          const ref = slice!.ref!;
          const params = toSliceRouteParams(ref);
          const pathPreview = formatPathPreview(slice?.definition?.includedPaths ?? []);
          return (
            <li
              className="flex items-start justify-between gap-3 px-4 py-3 transition hover:bg-slate-50 dark:hover:bg-zinc-950 sm:items-center"
              key={`${ref.account}/${ref.slice}`}
            >
              <div className="min-w-0 flex-1 sm:flex sm:items-baseline sm:gap-3">
                {params ? (
                  <Link
                    className="block truncate text-sm font-medium text-zinc-950 underline-offset-2 transition hover:text-sky-700 hover:underline dark:text-zinc-50 dark:hover:text-sky-300"
                    params={params as never}
                    to="/slices/$account/$slice"
                  >
                    {ref.account}/{ref.slice}
                  </Link>
                ) : null}
                <span className="mt-0.5 block truncate font-mono text-xs text-slate-500 dark:text-zinc-400 sm:mt-0">
                  {pathPreview}
                </span>
              </div>
              <div className="flex shrink-0 items-center gap-1.5">
                <VisibilityBadge visibility={slice?.definition?.visibility} />
                <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-700 dark:bg-zinc-800 dark:text-zinc-300">
                  {role}
                </span>
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
