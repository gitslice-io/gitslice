import { useEffect, useRef, type ReactNode } from "react";

import { cn } from "../lib/cn";
import { ActionMenu, type ActionMenuItem } from "./source/ActionMenu";

interface PageHeaderProps {
  /** Pass a <Breadcrumb .../> element. */
  breadcrumb?: ReactNode;
  /** Optional second line under the breadcrumb (e.g. an entity title + badge). */
  title?: ReactNode;
  /** Optional single always-visible action node (rendered left of the menu). */
  primaryAction?: ReactNode;
  /** Items for the right-aligned "⋯" dropdown. Empty/undefined => no dropdown. */
  actions?: ActionMenuItem[];
  /** Accessible label for the dropdown trigger. */
  menuLabel?: string;
  /** Optional full-width row rendered below the title/actions row. */
  tabs?: ReactNode;
}

export function PageHeader({
  breadcrumb,
  title,
  primaryAction,
  actions,
  menuLabel = "Actions",
  tabs
}: PageHeaderProps) {
  const headerRef = useRef<HTMLElement>(null);

  // Publish the sticky header's height so other sticky elements (e.g. the diff
  // file switcher) can pin themselves directly below it instead of being hidden
  // underneath. The breadcrumb can wrap to two lines on mobile, so measure it
  // rather than hardcoding an offset.
  useEffect(() => {
    const element = headerRef.current;
    if (!element || typeof ResizeObserver === "undefined") {
      return;
    }

    const root = document.documentElement;
    const update = () => {
      root.style.setProperty(
        "--page-header-height",
        `${element.offsetHeight}px`
      );
    };

    update();
    const observer = new ResizeObserver(update);
    observer.observe(element);

    return () => {
      observer.disconnect();
      root.style.removeProperty("--page-header-height");
    };
  }, []);

  return (
    <header
      className={cn(
        "sticky top-0 z-30 mb-4 flex flex-col gap-2 border-b border-slate-200 bg-slate-50/95 backdrop-blur",
        tabs ? "pt-3" : "py-3"
      )}
      ref={headerRef}
    >
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between sm:gap-3">
        <div className="min-w-0 sm:flex-1">
          {breadcrumb}
          {title ? <div className="mt-1 min-w-0">{title}</div> : null}
        </div>
        <div className="flex flex-wrap items-center gap-2 sm:flex-nowrap sm:shrink-0">
          {primaryAction}
          {actions && actions.length > 0 ? (
            <ActionMenu items={actions} label={menuLabel} />
          ) : null}
        </div>
      </div>
      {tabs ? <div className="-mb-px w-full">{tabs}</div> : null}
    </header>
  );
}
