import { Link } from "@tanstack/react-router";

import { breadcrumbSegments } from "./sourceUtils";

interface SourceBreadcrumbProps {
  account: string;
  repositoryPath: string;
  search: Record<string, string | undefined>;
}

export function SourceBreadcrumb({
  account,
  repositoryPath,
  search
}: SourceBreadcrumbProps) {
  const segments = breadcrumbSegments(account, repositoryPath);

  return (
    <nav aria-label="Source path" className="min-w-0 text-sm">
      <ol className="flex min-w-0 flex-wrap items-center gap-1 text-slate-500">
        <li className="font-mono text-slate-400">/</li>
        {segments.map((segment, index) => {
          const isLast = index === segments.length - 1;
          return (
            <li className="flex min-w-0 items-center gap-1" key={segment.repositoryPath}>
              {isLast ? (
                <span className="truncate font-medium text-zinc-950">
                  {segment.label}
                </span>
              ) : (
                <Link
                  className="truncate rounded-sm font-medium text-slate-600 underline-offset-4 hover:text-zinc-950 hover:underline"
                  params={
                    segment.routePath
                      ? ({ account, _splat: segment.routePath } as never)
                      : ({ account } as never)
                  }
                  search={search as never}
                  to={
                    segment.routePath
                      ? "/source/$account/$"
                      : "/source/$account"
                  }
                >
                  {segment.label}
                </Link>
              )}
              {!isLast ? <span className="font-mono text-slate-400">/</span> : null}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

