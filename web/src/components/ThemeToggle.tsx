import { useEffect } from "react";

import { cn } from "../lib/cn";
import { toggleTheme, watchSystemTheme } from "../theme";

interface ThemeToggleProps {
  className?: string;
  inverted?: boolean;
}

export function ThemeToggle({ className, inverted = false }: ThemeToggleProps) {
  useEffect(() => watchSystemTheme(), []);

  return (
    <button
      aria-label="Toggle color theme"
      className={cn(
        "group inline-flex size-9 shrink-0 items-center justify-center rounded-full border outline-none transition duration-200 active:scale-[0.96] focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2",
        inverted
          ? "border-zinc-300 bg-white/80 text-zinc-700 hover:bg-white focus-visible:ring-offset-slate-50 dark:border-white/10 dark:bg-white/[0.05] dark:text-zinc-300 dark:hover:bg-white/[0.1] dark:focus-visible:ring-offset-zinc-950"
          : "border-slate-200 bg-white text-slate-600 hover:border-slate-300 hover:bg-slate-50 hover:text-zinc-950 focus-visible:ring-offset-white dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:border-zinc-600 dark:hover:bg-zinc-800 dark:hover:text-white dark:focus-visible:ring-offset-zinc-950",
        className,
      )}
      onClick={toggleTheme}
      title="Toggle light and dark theme"
      type="button"
    >
      <svg
        aria-hidden="true"
        className="size-[1.1rem] dark:hidden"
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.8"
        viewBox="0 0 24 24"
      >
        <path d="M21 12.8A8.6 8.6 0 1 1 11.2 3 6.7 6.7 0 0 0 21 12.8Z" />
      </svg>
      <svg
        aria-hidden="true"
        className="hidden size-[1.1rem] dark:block"
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.8"
        viewBox="0 0 24 24"
      >
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.66 6.34l1.41-1.41" />
      </svg>
    </button>
  );
}
