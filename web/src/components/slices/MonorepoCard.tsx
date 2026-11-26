import { Link } from "@tanstack/react-router";

import type { Slice } from "../../api/types";
import { cn } from "../../lib/cn";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
import { buttonClassName } from "../ui/Button";
import { VisibilityBadge } from "./SlicePageParts";

interface MonorepoCardProps {
  account: string;
  homeSlice: Slice;
  className?: string;
}

export function MonorepoCard({ account, homeSlice, className }: MonorepoCardProps) {
  const homeRouteParams = toSliceRouteParams(homeSlice.ref);
  if (!homeRouteParams) {
    return null;
  }

  return (
    <section
      aria-label="Monorepo"
      className={cn(
        "rounded-lg border border-slate-200 bg-white p-4 shadow-sm shadow-slate-200/50 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/50 sm:p-5",
        className
      )}
    >
      <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Monorepo</h2>
            <span className="rounded border border-slate-200 bg-slate-50 px-1.5 py-px font-mono text-xs font-semibold text-slate-700 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-200">
              /{account}
            </span>
            <VisibilityBadge visibility={homeSlice.definition?.visibility} />
          </div>
          <p className="mt-1.5 max-w-2xl text-sm leading-6 text-slate-600 dark:text-zinc-400">
            One unified source tree for everything in <span className="whitespace-nowrap font-mono">/{account}</span>. Checks, automations,
            path owners, and secrets live on folders inside it.
          </p>
        </div>
        <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center lg:shrink-0">
          <Link
            className={cn(buttonClassName({ size: "md", variant: "primary" }), "col-span-2 justify-center sm:col-span-1")}
            params={homeRouteParams}
            to="/slices/$account/$slice"
          >
            Browse /{account}
          </Link>
          <Link
            className={cn(buttonClassName({ size: "md", variant: "secondary" }), "justify-center")}
            search={{ slice: `${account}:${homeRouteParams.slice}` }}
            to="/changesets"
          >
            Changesets
          </Link>
          <Link
            className={cn(buttonClassName({ size: "md", variant: "secondary" }), "justify-center")}
            params={homeRouteParams}
            search={{ policy: true }}
            to="/slices/$account/$slice"
          >
            Folder policy
          </Link>
        </div>
      </div>
    </section>
  );
}
