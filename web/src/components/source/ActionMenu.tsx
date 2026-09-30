import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent
} from "react";
import { createPortal } from "react-dom";

export interface ActionMenuItem {
  label: string;
  onSelect(): void;
  tone?: "default" | "danger";
  disabled?: boolean;
  title?: string;
}

interface MenuCoords {
  top: number;
  left: number;
}

export function ActionMenu({
  items,
  label = "Actions",
  align = "right"
}: {
  items: ActionMenuItem[];
  label?: string;
  align?: "left" | "right";
}): JSX.Element {
  const [open, setOpen] = useState(false);
  const [coords, setCoords] = useState<MenuCoords>({ top: 0, left: 0 });
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();

  // The menu is portaled to <body>, so Tab from the trigger never reaches it.
  // Follow the WAI-ARIA menu button pattern instead: focus moves into the menu
  // when it opens, arrow keys move between items, and closing returns focus
  // to the trigger.
  function menuItems() {
    return Array.from(
      menuRef.current?.querySelectorAll<HTMLButtonElement>(
        '[role="menuitem"]:not(:disabled)'
      ) ?? []
    );
  }

  function focusItem(target: "first" | "last" | "next" | "previous") {
    const enabled = menuItems();
    if (enabled.length === 0) {
      return;
    }
    const current = enabled.indexOf(document.activeElement as HTMLButtonElement);
    const index =
      target === "first"
        ? 0
        : target === "last"
          ? enabled.length - 1
          : target === "next"
            ? (current + 1) % enabled.length
            : (current - 1 + enabled.length) % enabled.length;
    enabled[index]?.focus({ preventScroll: true });
  }

  function closeMenu() {
    // Only take focus back if it was inside the menu; an outside click has
    // already put it where the user wants it.
    if (menuRef.current?.contains(document.activeElement)) {
      triggerRef.current?.focus({ preventScroll: true });
    }
    setOpen(false);
  }

  function onMenuKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        focusItem("next");
        break;
      case "ArrowUp":
        event.preventDefault();
        focusItem("previous");
        break;
      case "Home":
        event.preventDefault();
        focusItem("first");
        break;
      case "End":
        event.preventDefault();
        focusItem("last");
        break;
      case "Tab":
        // Refocus the trigger and let the browser's default Tab move on from
        // there, so focus continues in normal page order.
        closeMenu();
        break;
    }
  }

  useEffect(() => {
    if (open) {
      focusItem("first");
    }
  }, [open]);

  // Position the menu as a viewport-fixed element anchored to the trigger, so it
  // is never clipped by an overflow:auto ancestor (e.g. the directory table's
  // horizontal scroll wrapper). Measure the rendered menu and clamp it into the
  // viewport so it never runs off-screen regardless of where the trigger sits.
  useLayoutEffect(() => {
    if (!open || !triggerRef.current || !menuRef.current) {
      return;
    }

    const margin = 8;
    const trigger = triggerRef.current.getBoundingClientRect();
    const menu = menuRef.current.getBoundingClientRect();
    const viewportWidth = window.innerWidth;
    const viewportHeight = window.innerHeight;

    // Preferred horizontal anchor by `align`, then clamp within the viewport.
    let left = align === "left" ? trigger.left : trigger.right - menu.width;
    left = Math.min(
      Math.max(margin, left),
      Math.max(margin, viewportWidth - menu.width - margin)
    );

    // Below the trigger, or above it if there isn't room.
    let top = trigger.bottom + 4;
    if (top + menu.height > viewportHeight - margin) {
      top = Math.max(margin, trigger.top - menu.height - 4);
    }

    setCoords({ top: Math.round(top), left: Math.round(left) });
  }, [open, align]);

  useEffect(() => {
    if (!open) {
      return;
    }

    function onMouseDown(event: MouseEvent) {
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }
      if (
        !triggerRef.current?.contains(target) &&
        !menuRef.current?.contains(target)
      ) {
        setOpen(false);
      }
    }

    function onKeyDown(event: globalThis.KeyboardEvent) {
      if (event.key === "Escape") {
        closeMenu();
      }
    }

    // Any scroll (incl. inner scroll panes, capture phase) or resize would leave
    // the fixed menu detached from its trigger — just close it.
    function onReflow() {
      closeMenu();
    }

    document.addEventListener("mousedown", onMouseDown);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener("resize", onReflow);
    window.addEventListener("scroll", onReflow, true);

    return () => {
      document.removeEventListener("mousedown", onMouseDown);
      document.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("resize", onReflow);
      window.removeEventListener("scroll", onReflow, true);
    };
  }, [open]);

  return (
    <>
      <button
        aria-controls={open ? menuId : undefined}
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={label}
        className="inline-flex h-8 w-8 items-center justify-center rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 text-lg font-semibold leading-none text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
        onClick={() => setOpen((current) => !current)}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" && !open) {
            event.preventDefault();
            setOpen(true);
          }
        }}
        ref={triggerRef}
        type="button"
      >
        <span aria-hidden="true">⋮</span>
      </button>
      {open
        ? createPortal(
            <div
              aria-label={label}
              className="fixed z-50 max-h-[60vh] min-w-36 max-w-[calc(100vw-1rem)] overflow-auto rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-1 shadow-lg shadow-slate-900/10"
              id={menuId}
              onKeyDown={onMenuKeyDown}
              ref={menuRef}
              role="menu"
              style={{ top: coords.top, left: coords.left }}
            >
              {items.map((item) => {
                const isDanger = item.tone === "danger";
                return (
                  <button
                    className={[
                      "block w-full whitespace-nowrap rounded px-3 py-2 text-left text-sm font-medium transition disabled:cursor-not-allowed disabled:opacity-50",
                      isDanger
                        ? "text-rose-700 dark:text-rose-300 hover:bg-rose-50 dark:hover:bg-rose-950/30"
                        : "text-slate-700 dark:text-zinc-300 hover:bg-slate-50 dark:hover:bg-zinc-950"
                    ].join(" ")}
                    disabled={item.disabled}
                    key={item.label}
                    onClick={() => {
                      // Close (and refocus the trigger) before onSelect, so a
                      // dialog it opens keeps the focus it takes.
                      closeMenu();
                      item.onSelect();
                    }}
                    role="menuitem"
                    title={item.title}
                    type="button"
                  >
                    {item.label}
                  </button>
                );
              })}
            </div>,
            document.body
          )
        : null}
    </>
  );
}
