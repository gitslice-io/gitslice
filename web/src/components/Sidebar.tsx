import { Link } from "@tanstack/react-router";

import { cn } from "../lib/cn";
import { Surface } from "./ui";

const navItems = [
  { label: "Home", to: "/" },
  { label: "Slices", to: "/slices" },
  { label: "Changesets", to: "/changesets" }
] as const;

export function Sidebar() {
  return (
    <Surface
      as="aside"
      className="flex rounded-none text-on-surface md:min-h-[100dvh] md:w-60 md:flex-col"
      level="dim"
    >
      <div className="flex h-16 items-center px-4 font-serif text-base font-semibold text-on-surface md:px-6">
        Gitslice
      </div>
      <nav
        aria-label="Primary"
        className="flex flex-1 items-center gap-1 overflow-x-auto px-2 md:block md:px-3"
      >
        {navItems.map((item) => (
          <Link
            activeProps={{ "aria-current": "page" }}
            className={cn(
              "block rounded-sm px-3 py-2 font-label text-sm font-semibold text-on-surface-variant transition hover:bg-surface-container hover:text-primary",
              "aria-[current=page]:bg-primary/10 aria-[current=page]:text-primary"
            )}
            key={item.label}
            to={item.to}
          >
            {item.label}
          </Link>
        ))}
      </nav>
    </Surface>
  );
}
