import { createContext, useContext, type ReactNode } from "react";

// On phones, a file or a changeset page starts with its details folded away
// (breadcrumbs, paths, counts, settings) so the content comes first; one
// toggle brings them back. Large screens always show them.

interface MobileDetailsState {
  open: boolean;
  toggle(): void;
}

const MobileDetailsContext = createContext<MobileDetailsState | null>(null);

export function MobileDetailsProvider({
  children,
  onToggle,
  open
}: {
  children: ReactNode;
  onToggle(): void;
  open: boolean;
}) {
  return (
    <MobileDetailsContext.Provider value={{ open, toggle: onToggle }}>
      {children}
    </MobileDetailsContext.Provider>
  );
}

/** The page's fold state, or null outside a page that folds its details. */
export function useMobileDetails() {
  return useContext(MobileDetailsContext);
}

/**
 * A class that hides an element below the lg breakpoint while the page's
 * details are folded; "" when they are shown or the page does not fold.
 */
export function useMobileDetailsClass() {
  const state = useContext(MobileDetailsContext);
  return state && !state.open ? "max-lg:hidden" : "";
}

export function MobileDetailsToggle({ className }: { className?: string }) {
  const state = useContext(MobileDetailsContext);
  if (!state) {
    return null;
  }
  return (
    <button
      aria-expanded={state.open}
      className={[
        "inline-flex shrink-0 items-center gap-1 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2.5 py-1 text-xs font-medium text-slate-600 dark:text-zinc-400 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:text-zinc-950 dark:hover:text-zinc-50 lg:hidden",
        className ?? ""
      ].join(" ")}
      onClick={state.toggle}
      type="button"
    >
      {state.open ? "Hide details" : "Details"}
      <span aria-hidden="true" className={state.open ? "rotate-180" : ""}>
        ▾
      </span>
    </button>
  );
}
