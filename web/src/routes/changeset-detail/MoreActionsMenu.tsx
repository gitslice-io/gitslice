import type { ReactNode } from "react";

import { PortalMenu } from "../../components/ui/PortalMenu";
import { cn } from "../../lib/cn";

export function MoreActionsMenu({
  children,
  disabled,
  label = "More actions"
}: {
  children: ReactNode;
  disabled?: boolean;
  label?: string;
}) {
  return (
    <PortalMenu
      panelClassName="w-72 p-3"
      renderTrigger={({ open, ref, toggle }) => (
        <button
          ref={ref}
          aria-expanded={open}
          aria-haspopup="menu"
          aria-label={label}
          className="inline-flex h-8 items-center gap-1 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-xs font-medium text-slate-700 dark:text-zinc-300 transition hover:border-slate-400 dark:hover:border-zinc-600 hover:bg-slate-50 dark:hover:bg-zinc-950 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
          disabled={disabled}
          onClick={toggle}
          type="button"
        >
          <span>More</span>
          <span
            aria-hidden="true"
            className={cn(
              "text-[10px] leading-none transition-transform",
              open && "rotate-180"
            )}
          >
            ▾
          </span>
        </button>
      )}
    >
      {children}
    </PortalMenu>
  );
}
