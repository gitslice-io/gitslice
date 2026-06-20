import { Link, useRouterState } from "@tanstack/react-router";
import { useAuth, UserButton } from "@clerk/clerk-react";

import { cn } from "../lib/cn";
import { useSelection } from "../state/selection";
import { Badge, Surface, buttonClassName } from "./ui";

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
    pathname.startsWith("/dependencies") ||
    pathname.startsWith("/cs");
  const isDocActive = pathname.startsWith("/doc");

  return (
    <Surface
      as="header"
      className="sticky top-0 z-20 rounded-none bg-surface-container-low/90 px-3 backdrop-blur-[20px] sm:px-4 md:px-6"
      level="low"
    >
      <div className="mx-auto flex min-h-14 w-full max-w-[100rem] flex-wrap items-center justify-between gap-x-3 gap-y-2 py-2 sm:min-h-16 sm:flex-nowrap sm:py-0">
        <div className="flex min-w-0 items-center gap-3 sm:gap-5">
          <Link
            className="shrink-0 font-serif text-base font-semibold text-on-surface transition hover:text-primary"
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
                  "relative rounded-sm px-2.5 py-2 font-label text-sm font-semibold text-on-surface-variant transition hover:bg-surface-container hover:text-primary sm:px-3",
                  ((item.section === "slices" && isSlicesActive) ||
                    (item.section === "doc" && isDocActive)) &&
                    "bg-primary/10 text-primary hover:bg-primary/10 after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-full after:bg-primary"
                )}
                key={item.label}
                to={item.to}
              >
                {item.label}
              </Link>
            ))}
          </nav>
          {account ? (
            <div className="hidden min-w-0 text-right sm:block">
              <Badge variant="tertiary">Account</Badge>
              <div className="mt-1 truncate text-sm font-medium text-on-surface">
                {account}
              </div>
            </div>
          ) : null}
          {isLoaded && isSignedIn ? (
            <div className="shrink-0">
              <UserButton afterSignOutUrl="/login" />
            </div>
          ) : (
            <Link
              className={buttonClassName({
                className: "shrink-0",
                size: "sm",
                variant: "secondary"
              })}
              to="/login"
            >
              Sign in
            </Link>
          )}
        </div>
      </div>
    </Surface>
  );
}
