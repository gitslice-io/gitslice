import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { useAppAuth } from "../auth/AppAuthProvider";
import { formatDateTime } from "../lib/time";
import { useSelection } from "../state/selection";

interface MenuPosition {
  right: number;
  top: number;
}

function formatShortExpiry(expiresAt: string | null): string | null {
  return formatDateTime(expiresAt) || null;
}

// Top-bar account control for agent web sessions (in place of Clerk's
// UserButton). Shows a compact agent badge next to AccountSwitcher (avoiding
// duplicate account names) and renders its menu in a portal so sticky/blurred
// headers never clip or overlap it.
export function AgentAccountMenu() {
  const { agentAccount, agentExpiresAt, signOut, userId } = useAppAuth();
  const { account } = useSelection();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [pos, setPos] = useState<MenuPosition | null>(null);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);

  const displayAccount = account || agentAccount || userId || "agent";
  const expiryLabel = formatShortExpiry(agentExpiresAt);

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) {
      setPos(null);
      return;
    }
    const measure = () => {
      if (!triggerRef.current) {
        return;
      }
      const rect = triggerRef.current.getBoundingClientRect();
      setPos({
        top: rect.bottom + 8,
        right: Math.max(8, window.innerWidth - rect.right)
      });
    };
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }
    const inside = (target: EventTarget | null) =>
      target instanceof Node &&
      (containerRef.current?.contains(target) || menuRef.current?.contains(target));
    const handlePointerDown = (event: PointerEvent) => {
      if (!inside(event.target)) {
        setOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };
    const handleScroll = (event: Event) => {
      if (!inside(event.target)) {
        setOpen(false);
      }
    };
    document.addEventListener("pointerdown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    window.addEventListener("scroll", handleScroll, true);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
      window.removeEventListener("scroll", handleScroll, true);
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

  const menu =
    open && pos ? (
      <div
        className="fixed z-[70] w-64 max-w-[calc(100vw-1rem)] rounded-lg border border-slate-200 bg-white p-3 text-sm shadow-lg shadow-slate-200/60 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/50"
        ref={menuRef}
        role="menu"
        style={{ right: pos.right, top: pos.top }}
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
    ) : null;

  return (
    <div className="relative shrink-0" ref={containerRef}>
      <button
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={`Agent session: ${displayAccount}`}
        className="relative inline-flex size-8 shrink-0 items-center justify-center gap-1.5 rounded-full border border-emerald-300 bg-emerald-50 p-0 text-xs font-semibold text-emerald-900 transition hover:border-emerald-400 hover:bg-emerald-100 dark:border-emerald-800/70 dark:bg-emerald-950/50 dark:text-emerald-200 dark:hover:bg-emerald-950/80 sm:w-auto sm:px-2.5"
        onClick={() => setOpen((current) => !current)}
        ref={triggerRef}
        title={`Signed in as agent @${displayAccount}`}
        type="button"
      >
        <svg
          aria-hidden="true"
          className="size-3.5 shrink-0"
          fill="none"
          stroke="currentColor"
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth={2}
          viewBox="0 0 24 24"
        >
          <polyline points="4 17 10 11 4 5" />
          <line x1="12" x2="20" y1="19" y2="19" />
        </svg>
        <span
          aria-hidden
          className="absolute -right-0.5 -top-0.5 size-2 rounded-full bg-emerald-500 ring-2 ring-white dark:ring-zinc-950 sm:static sm:size-1.5 sm:shrink-0 sm:ring-0"
        />
        <span className="hidden text-[11px] font-semibold uppercase tracking-wide sm:inline">
          Agent
        </span>
      </button>

      {menu && typeof document !== "undefined" ? createPortal(menu, document.body) : null}
    </div>
  );
}
