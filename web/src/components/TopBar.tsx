import { Link } from "@tanstack/react-router";
import { UserButton } from "@clerk/clerk-react";

import { cn } from "../lib/cn";
import { useSelection } from "../state/selection";

const navItems = [
  { label: "home", to: "/" },
  { label: "doc", to: "/doc" }
] as const;

export function TopBar() {
  const { account } = useSelection();

  return (
    <header className="sticky top-0 z-10 border-b border-slate-200 bg-white/95 px-3 backdrop-blur sm:px-4 md:px-6">
      <div className="mx-auto flex min-h-14 w-full max-w-7xl flex-wrap items-center justify-between gap-x-3 gap-y-2 py-2 sm:min-h-16 sm:flex-nowrap sm:py-0">
        <div className="flex min-w-0 items-center gap-3 sm:gap-5">
          <Link
            className="shrink-0 text-sm font-semibold tracking-normal text-zinc-950"
            to="/"
          >
            Gitslice
          </Link>
        </div>
        <div className="flex min-w-0 items-center gap-2 sm:gap-3">
          <nav aria-label="Primary" className="flex items-center gap-1">
            {navItems.map((item) => (
              <Link
                activeOptions={{ exact: true }}
                activeProps={{ "aria-current": "page" }}
                className={cn(
                  "rounded-md px-2.5 py-2 text-sm font-medium text-slate-600 transition hover:bg-slate-100 hover:text-zinc-950 sm:px-3",
                  "aria-[current=page]:bg-zinc-950 aria-[current=page]:text-white"
                )}
                key={item.label}
                to={item.to}
              >
                {item.label}
              </Link>
            ))}
          </nav>
          {account ? (
            <div className="hidden min-w-0 text-right text-xs font-semibold text-slate-500 sm:block">
              Account
              <div className="truncate text-sm font-medium text-zinc-900">
                {account}
              </div>
            </div>
          ) : null}
          <div className="shrink-0">
            <UserButton afterSignOutUrl="/login" />
          </div>
        </div>
      </div>
    </header>
  );
}
