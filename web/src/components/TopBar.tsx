import { Link, useRouterState } from "@tanstack/react-router";
import { useAuth, UserButton } from "@clerk/tanstack-react-start";

import { cn } from "../lib/cn";
import { useSelection } from "../state/selection";

const navItems = [
  { label: "Slices", to: "/slices", section: "slices" },
  { label: "doc", to: "/doc", section: "doc" }
] as const;

export function TopBar() {
  const { account } = useSelection();
  const { isLoaded, isSignedIn } = useAuth();
  const pathname = useRouterState({
    select: (state) => state.location.pathname
  });
  const isSlicesActive =
    pathname === "/" ||
    pathname.startsWith("/slices") ||
    pathname.startsWith("/source") ||
    pathname.startsWith("/changesets") ||
    pathname.startsWith("/cs");
  const isDocActive = pathname.startsWith("/doc");

  return (
    <header className="border-b border-slate-200 bg-white/95 px-3 backdrop-blur sm:px-4 md:px-6">
      <div className="mx-auto flex min-h-14 w-full max-w-[100rem] flex-wrap items-center justify-between gap-x-3 gap-y-2 py-2 sm:min-h-16 sm:flex-nowrap sm:py-0">
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
                aria-current={
                  (item.section === "slices" && isSlicesActive) ||
                  (item.section === "doc" && isDocActive)
                    ? "page"
                    : undefined
                }
                className={cn(
                  "rounded-md px-2.5 py-2 text-sm font-medium text-slate-600 transition hover:bg-slate-100 hover:text-zinc-950 sm:px-3",
                  ((item.section === "slices" && isSlicesActive) ||
                    (item.section === "doc" && isDocActive)) &&
                    "bg-zinc-950 text-white hover:bg-zinc-950 hover:text-white"
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
          {!isLoaded ? (
            <div
              aria-hidden
              className="size-8 shrink-0 animate-pulse rounded-full bg-slate-200"
            />
          ) : isSignedIn ? (
            // Pin the avatar to the same 2rem box as the loading placeholder so
            // swapping from placeholder → UserButton (and Clerk's own internal
            // mount) causes no layout shift. Clerk's default trigger size is
            // larger than our placeholder, which is the residual jump.
            <div className="flex size-8 shrink-0 items-center justify-center">
              <UserButton
                appearance={{
                  elements: {
                    rootBox: "size-8",
                    userButtonBox: "size-8",
                    userButtonTrigger: "size-8 rounded-full p-0",
                    userButtonAvatarBox: "size-8"
                  }
                }}
              />
            </div>
          ) : (
            <Link
              className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
              to="/login"
            >
              Sign in
            </Link>
          )}
        </div>
      </div>
    </header>
  );
}
