import { Link } from "@tanstack/react-router";

import { cn } from "../lib/cn";

const navItems = [
  { label: "Home", to: "/" },
  { label: "Slices", to: "/slices" },
  { label: "Changesets", to: "/changesets" },
  { label: "Stacks", to: "/stacks" }
] as const;

export function Sidebar() {
  return (
    <aside className="flex border-b border-slate-200 bg-zinc-950 text-white md:min-h-[100dvh] md:w-60 md:flex-col md:border-b-0 md:border-r md:border-zinc-800">
      <div className="flex h-16 items-center px-4 text-sm font-semibold md:px-6">
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
              "block rounded-md px-3 py-2 text-sm font-medium text-zinc-300 transition hover:bg-white/10 hover:text-white",
              "aria-[current=page]:bg-white aria-[current=page]:text-zinc-950"
            )}
            key={item.label}
            to={item.to}
          >
            {item.label}
          </Link>
        ))}
      </nav>
    </aside>
  );
}
