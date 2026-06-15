import { useEffect, useRef, useState } from "react";

export interface ActionMenuItem {
  label: string;
  onSelect(): void;
  tone?: "default" | "danger";
  disabled?: boolean;
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
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) {
      return;
    }

    function onMouseDown(event: MouseEvent) {
      const root = rootRef.current;
      if (
        root &&
        event.target instanceof Node &&
        !root.contains(event.target)
      ) {
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

  return (
    <div className="relative inline-flex" ref={rootRef}>
      <button
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={label}
        className="inline-flex h-8 w-8 items-center justify-center rounded-md border border-slate-300 bg-white text-lg font-semibold leading-none text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
        onClick={() => setOpen((current) => !current)}
        type="button"
      >
        <span aria-hidden="true">⋮</span>
      </button>
      {open ? (
        <div
          className={[
            "absolute top-full z-30 mt-1 min-w-36 rounded-md border border-slate-200 bg-white p-1 shadow-lg shadow-slate-900/10",
            align === "left" ? "left-0" : "right-0"
          ].join(" ")}
          role="menu"
        >
          {items.map((item) => {
            const isDanger = item.tone === "danger";
            return (
              <button
                className={[
                  "block w-full rounded px-3 py-2 text-left text-sm font-medium transition disabled:cursor-not-allowed disabled:opacity-50",
                  isDanger
                    ? "text-rose-700 hover:bg-rose-50"
                    : "text-slate-700 hover:bg-slate-50"
                ].join(" ")}
                disabled={item.disabled}
                key={item.label}
                onClick={() => {
                  item.onSelect();
                  setOpen(false);
                }}
                role="menuitem"
                type="button"
              >
                {item.label}
              </button>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}
