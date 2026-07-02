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
  /** Optional full-width tab row rendered below the header. Never sticky: tabs
   * are arrival-time wayfinding, so they scroll away with the content. */
  tabs?: ReactNode;
  /** Pin the breadcrumb/actions row to the viewport top. Opt in only where the
   * header carries actions the user needs after scrolling (changeset review);
   * elsewhere the header scrolls away to give the content the viewport back. */
  sticky?: boolean;
}

export function PageHeader({
  breadcrumb,
  title,
  primaryAction,
  actions,
  menuLabel = "Actions",
  tabs,
  sticky = false
}: PageHeaderProps) {
  const headerRef = useRef<HTMLElement>(null);

  // Publish the pinned header's height so other sticky elements (e.g. the diff
  // file switcher) can pin themselves directly below it instead of being hidden
  // underneath. The breadcrumb can wrap to two lines on mobile, so measure it
  // rather than hardcoding an offset. Only a sticky header occludes anything,
  // so non-sticky headers don't publish.
  useEffect(() => {
    const element = headerRef.current;
    if (!sticky || !element || typeof ResizeObserver === "undefined") {
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
  }, [sticky]);

  return (
    <>
      <header
        className={cn(
          "flex flex-col gap-2 border-b border-slate-200 bg-slate-50/95 py-3 sm:flex-row sm:items-center sm:justify-between sm:gap-3",
          sticky && "sticky top-0 z-30 backdrop-blur",
          // With tabs below, the tab row's underline is the divider — except on
          // a sticky header, which needs its own edge once the tabs scroll away.
          tabs ? cn("pb-1", !sticky && "border-b-0") : "mb-4"
        )}
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
      {tabs ? <div className="mb-4">{tabs}</div> : null}
    </>
  );
}
