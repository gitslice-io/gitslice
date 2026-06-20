import { Link, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import type { Slice } from "../api/types";
import { useApi } from "../api/useApi";
import { Badge, Card, PageHeader, buttonClassName } from "../components/ui";
import { shortHash } from "../lib/objectId";
import {
  SliceLoadingBlock,
  SliceNotice,
  formatPathPreview,
  getErrorMessage,
  sliceDisplayName
} from "../components/slices/SlicePageParts";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { useSelection } from "../state/selection";

interface SlicesSearch {
  account?: string;
}

const PAGE_SIZE = 100;

export function SlicesPage() {
  const api = useApi();
  const selection = useSelection();
  const search = useSearch({ strict: false }) as SlicesSearch;
  const effectiveAccount = (search.account || selection.account || "").trim();

  const slicesQuery = useQuery({
    enabled: effectiveAccount.length > 0,
    queryKey: ["slices", effectiveAccount],
    queryFn: async () => {
      const slices: Slice[] = [];
      let cursor = "";

      do {
        const response = await api.listSlices({
          account: effectiveAccount,
          cursor,
          pageSize: PAGE_SIZE
        });

        slices.push(...(response.slices ?? []));
        cursor = response.nextCursor ?? "";
      } while (cursor);

      return slices;
    }
  });
  const slices = slicesQuery.data ?? [];

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        eyebrow="Slices"
        actions={
          <Link
            className={buttonClassName({ variant: "primary" })}
            to="/slices/new"
          >
            New slice
          </Link>
        }
        title={
          <span className="block break-words font-serif">
            {effectiveAccount ? `Slices for ${effectiveAccount}` : "Slices"}
          </span>
        }
        description="Definitions for slices under the selected account."
      />

      <div className="mt-8">
        {selection.isLoading ? (
          <SliceLoadingBlock />
        ) : selection.error ? (
          <SliceNotice title="Could not load your home account" tone="error">
            {getErrorMessage(selection.error)}
          </SliceNotice>
        ) : !effectiveAccount ? (
          <SliceNotice title="Select an account">
            Your signed-in session did not return a home account.
          </SliceNotice>
        ) : slicesQuery.isLoading ? (
          <SliceLoadingBlock />
        ) : slicesQuery.isError ? (
          <SliceNotice title="Could not load slices" tone="error">
            {getErrorMessage(slicesQuery.error)}
          </SliceNotice>
        ) : slices.length === 0 ? (
          <SliceNotice title="No slices returned">
            The server did not return any slices for this account.
          </SliceNotice>
        ) : (
          <div className="grid gap-3">
            {slices.map((slice) => {
              const paths = slice.definition?.includedPaths ?? [];
              const sliceId = slice.id ?? "";
              const routeParams = toSliceRouteParams(slice.ref);
              const visibility = slice.definition?.visibility || "unspecified";
              const isPublic = visibility.toLowerCase() === "public";

              return (
                <Card
                  className="grid gap-4 transition hover:bg-surface-container md:grid-cols-[minmax(14rem,1.35fr)_auto_minmax(8rem,0.4fr)_minmax(10rem,0.55fr)_minmax(14rem,1fr)] md:items-center"
                  key={sliceId || sliceDisplayName(slice)}
                  level="low"
                  padding="md"
                >
                  <div className="min-w-0">
                    <p className="font-label text-xs font-semibold uppercase text-on-surface-muted md:hidden">
                      Slice
                    </p>
                    <div className="mt-1 min-w-0 break-words text-base font-semibold text-on-surface md:mt-0">
                      {routeParams ? (
                        <Link
                          className="underline-offset-4 transition hover:text-primary hover:underline"
                          params={routeParams}
                          to="/slices/$account/$slice"
                        >
                          {sliceDisplayName(slice)}
                        </Link>
                      ) : (
                        sliceDisplayName(slice)
                      )}
                    </div>
                  </div>

                  <div className="flex items-center">
                    <Badge variant={isPublic ? "tertiary" : "neutral"}>
                      {visibility}
                    </Badge>
                  </div>

                  <MetadataItem label="Version">
                    {slice.definition?.version ?? "unknown"}
                  </MetadataItem>

                  <MetadataItem label="Definition hash" quiet>
                    {slice.definitionHash ? (
                      <span title={slice.definitionHash}>
                        {shortHash(slice.definitionHash)}
                      </span>
                    ) : (
                      "none"
                    )}
                  </MetadataItem>

                  <MetadataItem label="Included paths">
                    <span className="font-semibold text-on-surface">
                      {paths.length}
                    </span>{" "}
                    <span>{formatPathPreview(paths)}</span>
                  </MetadataItem>
                </Card>
              );
            })}
          </div>
        )}
      </div>
    </section>
  );
}

function MetadataItem({
  children,
  label,
  quiet = false
}: {
  children: ReactNode;
  label: string;
  quiet?: boolean;
}) {
  return (
    <div className="min-w-0">
      <p className="font-label text-xs font-semibold uppercase text-on-surface-muted">
        {label}
      </p>
      <div
        className={[
          "mt-1 min-w-0 break-all text-sm",
          quiet
            ? "font-mono text-xs text-on-surface-muted"
            : "text-on-surface-variant"
        ].join(" ")}
      >
        {children}
      </div>
    </div>
  );
}
