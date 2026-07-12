import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

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
  const [open, setOpen] = useState(false);
  const [coords, setCoords] = useState({ top: 0, left: 0 });
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    if (!open || !triggerRef.current || !menuRef.current) {
      return;
    }

    const margin = 8;
    const trigger = triggerRef.current.getBoundingClientRect();
    const menu = menuRef.current.getBoundingClientRect();
    const viewportWidth = window.innerWidth;
    const viewportHeight = window.innerHeight;

    let left = trigger.right - menu.width;
    left = Math.min(
      Math.max(margin, left),
      Math.max(margin, viewportWidth - menu.width - margin)
    );

    let top = trigger.bottom + 4;
    if (top + menu.height > viewportHeight - margin) {
      top = Math.max(margin, trigger.top - menu.height - 4);
    }

    setCoords({ top: Math.round(top), left: Math.round(left) });
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }

    const onPointerDown = (event: globalThis.MouseEvent) => {
      const target = event.target;
      if (
        target instanceof Node &&
        !triggerRef.current?.contains(target) &&
        !menuRef.current?.contains(target)
      ) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };
    const onReflow = () => {
      setOpen(false);
    };

    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener("resize", onReflow);
    window.addEventListener("scroll", onReflow, true);

    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("resize", onReflow);
      window.removeEventListener("scroll", onReflow, true);
    };
  }, [open]);

  return (
    <>
      <button
        ref={triggerRef}
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={label}
        className="inline-flex h-8 items-center gap-1 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-xs font-medium text-slate-700 dark:text-zinc-300 transition hover:border-slate-400 dark:hover:border-zinc-600 hover:bg-slate-50 dark:hover:bg-zinc-950 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
        disabled={disabled}
        onClick={() => setOpen((value) => !value)}
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
      {open
        ? createPortal(
            <div
              className="fixed z-50 w-72 max-w-[calc(100vw-1rem)] rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-3 shadow-lg shadow-slate-900/10"
              ref={menuRef}
              role="menu"
              style={{ top: coords.top, left: coords.left }}
            >
              {children}
            </div>,
            document.body
          )
        : null}
    </>
  );
}