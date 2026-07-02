import { useAuth } from "@clerk/tanstack-react-start";
import { Link } from "@tanstack/react-router";
import type { JSX } from "react";

import { cn } from "../lib/cn";

export type SliceTabId = "files" | "changesets" | "conversations" | "settings";

const tabs = [
  { id: "files", label: "Files", requiresAuth: false },
  { id: "changesets", label: "Changesets", requiresAuth: false },
  { id: "conversations", label: "Conversations", requiresAuth: true },
  { id: "settings", label: "Settings", requiresAuth: true }
] satisfies {
  id: SliceTabId;
  label: string;
  requiresAuth: boolean;
}[];

export function SliceTabs({
  active,
  params,
  sliceLabel
}: {
  active: SliceTabId;
  /** Route params for /slices/$account/$slice(...) links. */
  params: { account: string; slice: string };
  /** Canonical "account:slice" handle, used for /changesets?slice=... */
  sliceLabel: string;
}): JSX.Element {
  const { isLoaded, isSignedIn } = useAuth();
  const showSignedInTabs = Boolean(isLoaded && isSignedIn);
  const visibleTabs = showSignedInTabs
    ? tabs
    : tabs.filter((tab) => !tab.requiresAuth);

  return (
    <div className="border-b border-slate-200">
      <nav
        aria-label="Slice sections"
        className="-mb-px flex min-w-0 gap-1 overflow-x-auto"
      >
        {visibleTabs.map((tab) => {
          const ariaCurrent = active === tab.id ? ("page" as const) : undefined;
          const commonProps = {
            "aria-current": ariaCurrent,
            className: tabClass(active === tab.id),
            children: tab.label
          };

          if (tab.id === "changesets") {
            return (
              <Link
                {...commonProps}
                key={tab.id}
                search={{ slice: sliceLabel } as never}
                to="/changesets"
              />
            );
          }

          if (tab.id === "conversations") {
            return (
              <Link
                {...commonProps}
                key={tab.id}
                params={params as never}
                to="/slices/$account/$slice/agents"
              />
            );
          }

          if (tab.id === "settings") {
            return (
              <Link
                {...commonProps}
                key={tab.id}
                params={params as never}
                to="/slices/$account/$slice/settings"
              />
            );
          }

          return (
            <Link
              {...commonProps}
              key={tab.id}
              params={params as never}
              to="/slices/$account/$slice"
            />
          );
        })}
      </nav>
    </div>
  );
}

function tabClass(active: boolean) {
  return cn(
    "shrink-0 border-b-2 px-2.5 py-2 text-sm font-semibold transition sm:px-3",
    active
      ? "border-zinc-950 text-zinc-950"
      : "border-transparent text-slate-600 hover:border-slate-300 hover:text-zinc-950"
  );
}
