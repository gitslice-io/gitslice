import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { kindLabel, type Membership } from "../lib/accounts";
import { cn } from "../lib/cn";
import { useSelection } from "../state/selection";
import { useMyInvitations } from "./slices/PendingInvitations";

// The account switcher in the top bar: like GitHub's context switcher, it
// picks which of the viewer's accounts (personal, organizations, claimed
// agents) Home and "New slice" work in.
export function AccountSwitcher() {
  const { activeAccount, activeMembership, memberships, setActiveAccount } = useSelection();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const [place, setPlace] = useState<MenuPlace | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const invitations = useMyInvitations().data ?? [];

  // The menu is drawn in a portal, fixed to the viewport: inside the top bar it
  // would sit under the page's sticky header (the bar's blur makes it its own
  // stacking context), and anchored to the button it ran off a phone's screen.
  useLayoutEffect(() => {
    if (!open || !trigger.current) {
      setPlace(null);
      return;
    }
    const measure = () => {
      if (!trigger.current) {
        return;
      }
      setPlace(menuPlace(trigger.current.getBoundingClientRect(), window.innerWidth, window.innerHeight));
    };
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, [open]);

  useEffect(() => {
    if (!open) {
      setFilter("");
      return;
    }
    function inside(target: EventTarget | null) {
      return target instanceof Node && (root.current?.contains(target) || menu.current?.contains(target));
    }
    function onPointerDown(event: PointerEvent) {
      if (!inside(event.target)) {
        setOpen(false);
      }
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setOpen(false);
      }
    }
    // Scrolling the page moves the button away from the menu; scrolling the
    // menu itself is fine.
    function onScroll(event: Event) {
      if (!inside(event.target)) {
        setOpen(false);
      }
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("scroll", onScroll, true);
    };
  }, [open]);

  if (!activeAccount) {
    return null;
  }

  // A long list (people who claimed many agents) gets a filter, as GitHub's does.
  const showFilter = memberships.length > FILTER_FROM;
  const needle = filter.trim().toLowerCase();
  const shown = needle ? memberships.filter((m) => m.account.toLowerCase().includes(needle)) : memberships;
  const groups: [string, Membership[]][] = [
    ["Personal", shown.filter((m) => m.kind === "personal")],
    ["Organizations", shown.filter((m) => m.kind === "organization")],
    ["Agents", shown.filter((m) => m.kind === "agent")],
    ["Other", shown.filter((m) => !["personal", "organization", "agent"].includes(m.kind))]
  ];

  function choose(account: string) {
    setActiveAccount(account);
    setOpen(false);
    void navigate({ to: "/" });
  }

  return (
    <div className="relative" ref={root}>
      <button
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={`Switch account (current: ${activeAccount})`}
        className="flex max-w-48 items-center gap-2 rounded-md border border-slate-200 px-2 py-1.5 text-left transition hover:bg-slate-50 dark:border-zinc-800 dark:hover:bg-zinc-900"
        onClick={() => setOpen((current) => !current)}
        ref={trigger}
        type="button"
      >
        <AccountAvatar account={activeAccount} kind={activeMembership?.kind ?? ""} />
        <span className="hidden min-w-0 sm:block">
          <span className="block truncate text-sm font-medium text-zinc-900 dark:text-zinc-100">{activeAccount}</span>
          <span className="block text-[11px] leading-3 text-slate-500 dark:text-zinc-500">
            {kindLabel(activeMembership?.kind ?? "")}
          </span>
        </span>
        {invitations.length > 0 ? (
          <span
            aria-label={`${invitations.length} pending invitation${invitations.length === 1 ? "" : "s"}`}
            className="rounded-full bg-sky-600 px-1.5 text-[10px] font-semibold leading-4 text-white"
          >
            {invitations.length}
          </span>
        ) : null}
        <span aria-hidden className="text-xs text-slate-400">
          ▾
        </span>
      </button>
      {open && place ? createPortal(
        <div
          className="fixed z-[100] flex flex-col overflow-hidden rounded-md border border-slate-200 bg-white shadow-lg shadow-slate-900/10 dark:border-zinc-800 dark:bg-zinc-900"
          ref={menu}
          role="menu"
          style={{ left: place.left, maxHeight: place.maxHeight, top: place.top, width: place.width }}
        >
          <p className="shrink-0 border-b border-slate-200 px-3 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
            Switch dashboard context
          </p>
          {showFilter ? (
            <div className="shrink-0 border-b border-slate-200 p-2 dark:border-zinc-800">
              <input
                aria-label="Find an account"
                autoFocus
                className="h-8 w-full rounded-md border border-slate-300 bg-white px-2 text-sm text-zinc-950 outline-none focus:border-slate-500 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-50"
                onChange={(event) => setFilter(event.target.value)}
                placeholder="Find an account"
                type="search"
                value={filter}
              />
            </div>
          ) : null}
          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain py-1">
            {shown.length === 0 ? (
              <p className="px-3 py-2 text-sm text-slate-500 dark:text-zinc-400">No account matches.</p>
            ) : null}
            {groups
              .filter(([, items]) => items.length > 0)
              .map(([title, items]) => (
                <div key={title}>
                  {groups.filter(([, other]) => other.length > 0).length > 1 ? (
                    <p className="px-3 pb-1 pt-2 text-[11px] font-semibold uppercase tracking-wide text-slate-400 dark:text-zinc-500">
                      {title}
                    </p>
                  ) : null}
                  {items.map((m) => (
                    <button
                      aria-checked={m.account === activeAccount}
                      className={cn(
                        "flex w-full items-center gap-2 px-3 py-2 text-left text-sm transition hover:bg-slate-50 dark:hover:bg-zinc-950",
                        m.account === activeAccount && "bg-slate-50 dark:bg-zinc-950"
                      )}
                      key={m.account}
                      onClick={() => choose(m.account)}
                      role="menuitemradio"
                      type="button"
                    >
                      <AccountAvatar account={m.account} kind={m.kind} />
                      <span className="min-w-0 flex-1 truncate font-medium text-zinc-900 dark:text-zinc-100">{m.account}</span>
                      {m.role ? <span className="text-xs text-slate-500 dark:text-zinc-400">{m.role}</span> : null}
                      <span aria-hidden className="w-4 text-right text-sky-600">
                        {m.account === activeAccount ? "✓" : ""}
                      </span>
                    </button>
                  ))}
                </div>
              ))}
          </div>
          <div className="shrink-0 border-t border-slate-200 py-1 dark:border-zinc-800">
            {invitations.length > 0 ? (
              <Link
                className="block px-3 py-2 text-sm font-medium text-sky-700 transition hover:bg-slate-50 dark:text-sky-300 dark:hover:bg-zinc-950"
                onClick={() => setOpen(false)}
                role="menuitem"
                to="/"
              >
                {invitations.length === 1 ? "1 pending invitation" : `${invitations.length} pending invitations`}
              </Link>
            ) : null}
            <Link
              className="block px-3 py-2 text-sm text-slate-700 transition hover:bg-slate-50 dark:text-zinc-300 dark:hover:bg-zinc-950"
              onClick={() => setOpen(false)}
              role="menuitem"
              to="/organizations/new"
            >
              New organization
            </Link>
            <Link
              className="block px-3 py-2 text-sm text-slate-700 transition hover:bg-slate-50 dark:text-zinc-300 dark:hover:bg-zinc-950"
              onClick={() => setOpen(false)}
              params={{ account: activeAccount }}
              role="menuitem"
              to="/accounts/$account"
            >
              {activeMembership?.kind === "organization" ? "Organization profile and people" : "Your profile"}
            </Link>
          </div>
        </div>,
        document.body
      ) : null}
    </div>
  );
}

const FILTER_FROM = 7;
const MENU_WIDTH = 288;
const MARGIN = 8;

export interface MenuPlace {
  left: number;
  maxHeight: number;
  top: number;
  width: number;
}

// menuPlace puts the menu under the button, right-aligned with it when there is
// room, and always inside the viewport: as wide as fits, and no taller than the
// space below (the list scrolls).
export function menuPlace(button: { bottom: number; right: number }, viewportWidth: number, viewportHeight: number): MenuPlace {
  const width = Math.min(MENU_WIDTH, viewportWidth - 2 * MARGIN);
  const left = Math.min(Math.max(MARGIN, button.right - width), viewportWidth - width - MARGIN);
  const top = Math.round(button.bottom + 4);
  return { left: Math.round(left), maxHeight: Math.max(160, viewportHeight - top - MARGIN), top, width };
}

export function AccountAvatar({ account, kind, size = "sm" }: { account: string; kind: string; size?: "sm" | "lg" }) {
  return (
    <span
      aria-hidden
      className={cn(
        "grid shrink-0 place-items-center font-semibold uppercase text-white",
        size === "lg" ? "size-12 text-lg" : "size-6 text-[11px]",
        // Organizations are square, people and agents round, as on GitHub.
        kind === "organization" ? "rounded-md bg-indigo-600" : kind === "agent" ? "rounded-full bg-teal-600" : "rounded-full bg-zinc-700"
      )}
    >
      {account.slice(0, 1)}
    </span>
  );
}
