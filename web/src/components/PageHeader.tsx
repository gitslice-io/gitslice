import { useEffect, useRef, type ReactNode } from "react";

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
}

export function PageHeader({
  breadcrumb,
  title,
  primaryAction,
  actions,
  menuLabel = "Actions"
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
      className="sticky top-0 z-30 mb-4 flex flex-col gap-2 border-b border-slate-200 dark:border-zinc-800 bg-slate-50/95 dark:bg-zinc-950/95 py-3 backdrop-blur sm:flex-row sm:items-center sm:justify-between sm:gap-3"
      ref={headerRef}
    >
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
    </header>
  );
}
