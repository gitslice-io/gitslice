import { Link } from "@tanstack/react-router";

export interface Crumb {
  label: string;
  to?: string;
  params?: unknown;
  search?: unknown;
}

export function Breadcrumb({ items }: { items: Crumb[] }): JSX.Element {
  return (
    <nav aria-label="Breadcrumb" className="min-w-0 font-label text-sm">
      <ol className="flex min-w-0 max-w-full flex-wrap items-center gap-x-1.5 gap-y-1">
        {items.map((item, index) => {
          const isLast = index === items.length - 1;

          return (
            <li
              className="flex min-w-0 max-w-full items-center gap-x-1.5"
              key={`${index}-${item.label}`}
            >
              {index > 0 ? (
                <span aria-hidden="true" className="shrink-0 text-tertiary/70">
                  &rsaquo;
                </span>
              ) : null}
              {item.to && !isLast ? (
                <Link
                  className="min-w-0 truncate break-all font-medium text-on-surface-variant transition hover:text-primary"
                  params={item.params as never}
                  search={item.search as never}
                  to={item.to as never}
                >
                  {item.label}
                </Link>
              ) : (
                <span className="min-w-0 truncate break-all font-semibold text-on-surface">
                  {item.label}
                </span>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
