import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

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
  const root = useRef<HTMLDivElement>(null);
  const invitations = useMyInvitations().data ?? [];

  useEffect(() => {
    if (!open) {
      return;
    }
    function onMouseDown(event: MouseEvent) {
      if (event.target instanceof Node && !root.current?.contains(event.target)) {
        setOpen(false);
      }
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", onMouseDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onMouseDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  if (!activeAccount) {
    return null;
  }

  const groups: [string, Membership[]][] = [
    ["Personal", memberships.filter((m) => m.kind === "personal")],
    ["Organizations", memberships.filter((m) => m.kind === "organization")],
    ["Agents", memberships.filter((m) => m.kind === "agent")],
    ["Other", memberships.filter((m) => !["personal", "organization", "agent"].includes(m.kind))]
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
      {open ? (
        <div
          className="absolute right-0 z-50 mt-1 w-72 overflow-hidden rounded-md border border-slate-200 bg-white shadow-lg shadow-slate-900/10 dark:border-zinc-800 dark:bg-zinc-900"
          role="menu"
        >
          <p className="border-b border-slate-200 px-3 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
            Switch dashboard context
          </p>
          <div className="max-h-[60vh] overflow-auto py-1">
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
          <div className="border-t border-slate-200 py-1 dark:border-zinc-800">
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
        </div>
      ) : null}
    </div>
  );
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
