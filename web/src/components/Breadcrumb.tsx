import { Link } from "@tanstack/react-router";

export interface Crumb {
  label: string;
  to?: string;
  params?: unknown;
  search?: unknown;
}

export function Breadcrumb({ items }: { items: Crumb[] }): JSX.Element {
  return (
    <nav aria-label="Breadcrumb" className="min-w-0 text-sm">
      <ol className="flex min-w-0 max-w-full flex-wrap items-center gap-x-1.5 gap-y-1">
        {items.map((item, index) => {
          const isLast = index === items.length - 1;

          return (
            <li
              className="flex min-w-0 max-w-full items-center gap-x-1.5"
              key={`${index}-${item.label}`}
            >
              {index > 0 ? (
                <span aria-hidden="true" className="shrink-0 text-slate-400 dark:text-zinc-500">
                  &rsaquo;
                </span>
              ) : null}
              {item.to && !isLast ? (
                <Link
                  className="min-w-0 truncate break-all font-medium text-slate-600 dark:text-zinc-400 transition hover:text-zinc-950 dark:hover:text-zinc-50"
                  params={item.params as never}
                  search={item.search as never}
                  to={item.to as never}
                >
                  {item.label}
                </Link>
              ) : (
                <span className="min-w-0 truncate break-all font-semibold text-zinc-950 dark:text-zinc-50">
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
