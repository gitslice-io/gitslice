import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
  type RefObject
} from "react";
import { createPortal } from "react-dom";

import { cn } from "../../lib/cn";

export interface PortalMenuRenderTriggerProps {
  open: boolean;
  ref: RefObject<HTMLButtonElement>;
  toggle: () => void;
}

export interface PortalMenuProps {
  align?: "left" | "right";
  children: ReactNode | ((controls: { close: () => void }) => ReactNode);
  panelClassName?: string;
  renderTrigger: (props: PortalMenuRenderTriggerProps) => ReactNode;
}

// Viewport-clamped dropdown rendered in document.body so overflow:auto
// ancestors never clip it. Closes on outside click, Escape, scroll, or resize.
export function PortalMenu({
  align = "right",
  children,
  panelClassName,
  renderTrigger
}: PortalMenuProps) {
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

    let left = align === "left" ? trigger.left : trigger.right - menu.width;
    left = Math.min(
      Math.max(margin, left),
      Math.max(margin, viewportWidth - menu.width - margin)
    );

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

    const onMouseDown = (event: MouseEvent) => {
      const target = event.target;
      if (
        target instanceof Node &&
        !triggerRef.current?.contains(target) &&
        !menuRef.current?.contains(target)
      ) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };
    const onReflow = () => {
      setOpen(false);
    };

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

  const close = () => setOpen(false);
  const toggle = () => setOpen((current) => !current);

  return (
    <>
      {renderTrigger({ open, ref: triggerRef, toggle })}
      {open
        ? createPortal(
            <div
              className={cn(
                "fixed z-50 max-w-[calc(100vw-1rem)] rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-lg shadow-slate-900/10",
                panelClassName
              )}
              ref={menuRef}
              role="menu"
              style={{ top: coords.top, left: coords.left }}
            >
              {typeof children === "function" ? children({ close }) : children}
            </div>,
            document.body
          )
        : null}
    </>
  );
}
