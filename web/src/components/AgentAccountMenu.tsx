import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { useAppAuth } from "../auth/AppAuthProvider";
import { useSelection } from "../state/selection";

function formatShortExpiry(expiresAt: string | null): string | null {
  if (!expiresAt) {
    return null;
  }
  const date = new Date(expiresAt);
  if (Number.isNaN(date.getTime())) {
    return null;
  }
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit"
  });
}

// Top-bar account control for agent web sessions (in place of Clerk's
// UserButton). Shows the active agent account, session expiry, and sign-out.
export function AgentAccountMenu() {
  const { agentAccount, agentExpiresAt, signOut, userId } = useAppAuth();
  const { account } = useSelection();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);

  const displayAccount = account || agentAccount || userId || "agent";
  const expiryLabel = formatShortExpiry(agentExpiresAt);

  useEffect(() => {
    if (!open) {
      return;
    }
    const handlePointerDown = (event: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("mousedown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [open]);

  async function handleSignOut() {
    setSigningOut(true);
    try {
      await signOut();
      setOpen(false);
      void navigate({ replace: true, to: "/login" });
    } finally {
      setSigningOut(false);
    }
  }

  return (
    <div className="relative" ref={containerRef}>
      <button
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={`Agent session: ${displayAccount}`}
        className="inline-flex h-8 items-center gap-1.5 rounded-full border border-emerald-300 bg-emerald-50 px-2.5 text-xs font-semibold text-emerald-900 transition hover:border-emerald-400 hover:bg-emerald-100 dark:border-emerald-800/70 dark:bg-emerald-950/50 dark:text-emerald-200 dark:hover:bg-emerald-950/80"
        onClick={() => setOpen((current) => !current)}
        title={`Signed in as agent ${displayAccount}`}
        type="button"
      >
        <span aria-hidden className="size-1.5 rounded-full bg-emerald-500" />
        <span className="max-w-[8rem] truncate">{displayAccount}</span>
        <span className="hidden rounded bg-emerald-200/70 px-1 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-emerald-900 dark:bg-emerald-900/70 dark:text-emerald-200 sm:inline">
          agent
        </span>
      </button>

      {open ? (
        <div
          className="absolute right-0 z-50 mt-2 w-64 rounded-lg border border-slate-200 bg-white p-3 text-sm shadow-lg shadow-slate-200/60 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/50"
          role="menu"
        >
          <div className="border-b border-slate-100 pb-2.5 dark:border-zinc-800">
            <p className="text-[11px] font-semibold uppercase tracking-wide text-emerald-700 dark:text-emerald-400">
              Agent web session
            </p>
            <p className="mt-0.5 truncate font-semibold text-zinc-950 dark:text-zinc-50">
              @{displayAccount}
            </p>
            {userId ? (
              <p className="mt-0.5 truncate font-mono text-xs text-slate-500 dark:text-zinc-400">
                {userId}
              </p>
            ) : null}
            {expiryLabel ? (
              <p className="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                Expires {expiryLabel}
              </p>
            ) : null}
          </div>

          <div className="mt-2 flex flex-col gap-1">
            {account || agentAccount ? (
              <Link
                className="rounded-md px-2.5 py-1.5 text-left text-sm font-medium text-slate-700 transition hover:bg-slate-100 dark:text-zinc-300 dark:hover:bg-zinc-800"
                onClick={() => setOpen(false)}
                params={{ account: account || agentAccount || "" }}
                role="menuitem"
                to="/accounts/$account"
              >
                Agent account
              </Link>
            ) : null}
            <button
              className="rounded-md px-2.5 py-1.5 text-left text-sm font-medium text-rose-700 transition hover:bg-rose-50 disabled:opacity-50 dark:text-rose-300 dark:hover:bg-rose-950/40"
              disabled={signingOut}
              onClick={() => void handleSignOut()}
              role="menuitem"
              type="button"
            >
              {signingOut ? "Signing out..." : "Sign out & revoke session"}
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
