import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import { useApi } from "../api/useApi";
import {
  IncludedPathLinks,
  SliceLoadingBlock,
  SliceMetadataGrid,
  SliceNotice,
  SlicePageHeader,
  SlicePanel,
  VisibilityBadge,
  getErrorMessage,
  sliceDisplayName,
  sourceTargetForIncludedPath
} from "../components/slices/SlicePageParts";
import { useSelection } from "../state/selection";

interface SliceParams {
  id?: string;
}

interface GitCloneEnv extends ImportMetaEnv {
  readonly VITE_GITSLICE_GIT_HTTP_BASE_URL?: string;
}

const DIRECTORY_PAGE_SIZE = 25;

export function SliceDetailPage() {
  const api = useApi();
  const { ref } = useSelection();
  const params = useParams({ strict: false }) as SliceParams;
  const sliceId = params.id ?? "";

  const sliceQuery = useQuery({
    enabled: sliceId.length > 0,
    queryKey: ["slice", sliceId],
    queryFn: () => api.getSlice({ sliceId })
  });

  const refQuery = useQuery({
    enabled: ref.trim().length > 0,
    queryKey: ["ref", ref],
    queryFn: () => api.getRef({ refName: ref.trim() })
  });

  const slice = sliceQuery.data;
  const directoryQuery = useQuery({
    enabled:
      Boolean(refQuery.data?.commitId) &&
      Boolean(slice?.ref?.account) &&
      Boolean(slice?.ref?.slice),
    queryKey: [
      "sliceDirectory",
      slice?.ref?.account,
      slice?.ref?.slice,
      refQuery.data?.commitId
    ],
    queryFn: () =>
      api.listDirectory({
        commitId: refQuery.data?.commitId,
        pageSize: DIRECTORY_PAGE_SIZE,
        path: "",
        slice: slice?.ref
      })
  });

  if (sliceQuery.isLoading) {
    return (
      <section className="mx-auto w-full max-w-7xl">
        <SliceLoadingBlock />
      </section>
    );
  }

  if (sliceQuery.isError) {
    return (
      <section className="mx-auto w-full max-w-7xl">
        <SlicePageHeader title="Slice Detail" />
        <div className="mt-8">
          <SliceNotice title="Could not load slice" tone="error">
            {getErrorMessage(sliceQuery.error)}
          </SliceNotice>
        </div>
      </section>
    );
  }

  if (!slice) {
    return (
      <section className="mx-auto w-full max-w-7xl">
        <SlicePageHeader title="Slice Detail" />
        <div className="mt-8">
          <SliceNotice title="Slice not found">
            No slice was returned for id {sliceId || "unknown"}.
          </SliceNotice>
        </div>
      </section>
    );
  }

  const definition = slice.definition;
  const includedPaths = definition?.includedPaths ?? [];
  const gitCloneHint = buildGitCloneHint(slice.ref?.account, slice.ref?.slice);

  return (
    <section className="mx-auto w-full max-w-7xl">
      <SlicePageHeader
        actions={
          <Link
            className="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
            params={{ id: sliceId }}
            to="/slices/$id/settings"
          >
            Settings
          </Link>
        }
        title={sliceDisplayName(slice)}
        description="Current slice definition and projected source view."
      />

      <div className="mt-8 space-y-6">
        <SlicePanel>
          <SliceMetadataGrid
            rows={[
              {
                label: "Visibility",
                value: <VisibilityBadge visibility={definition?.visibility} />
              },
              {
                label: "Version",
                value: definition?.version ?? "unknown"
              },
              {
                label: "Definition hash",
                value: (
                  <code className="font-mono text-xs">
                    {slice.definitionHash || "none"}
                  </code>
                )
              },
              {
                label: "Slice id",
                value: (
                  <code className="font-mono text-xs">{slice.id || sliceId}</code>
                )
              }
            ]}
          />
        </SlicePanel>

        <SlicePanel className="space-y-4">
          <div>
            <h2 className="text-base font-semibold text-zinc-950">
              Included paths
            </h2>
            <p className="mt-1 text-sm leading-6 text-slate-600">
              Each path opens in the source browser for its account-rooted path.
            </p>
          </div>
          <IncludedPathLinks
            fallbackAccount={slice.ref?.account}
            paths={includedPaths}
          />
        </SlicePanel>

        <SlicePanel className="space-y-3">
          <div>
            <h2 className="text-base font-semibold text-zinc-950">
              Git clone
            </h2>
            <p className="mt-1 text-sm leading-6 text-slate-600">
              Git smart HTTP supports clone and fetch when the deployment exposes
              the optional Git HTTP server.
            </p>
          </div>
          <div className="rounded-md border border-slate-200 bg-slate-50 p-3">
            <code className="break-all font-mono text-xs text-zinc-950">
              {gitCloneHint.url}
            </code>
            {!gitCloneHint.configured ? (
              <p className="mt-2 text-xs text-slate-500">
                Configure VITE_GITSLICE_GIT_HTTP_BASE_URL to show the concrete
                host for this deployment.
              </p>
            ) : null}
          </div>
        </SlicePanel>

        <SlicePanel className="space-y-4">
          <div>
            <h2 className="text-base font-semibold text-zinc-950">
              Projected source
            </h2>
            <p className="mt-1 text-sm leading-6 text-slate-600">
              Root directory entries filtered through this slice on ref{" "}
              <span className="font-medium text-zinc-950">{ref || "main"}</span>.
            </p>
          </div>

          {refQuery.isError ? (
            <SliceNotice title="Could not resolve ref" tone="error">
              {getErrorMessage(refQuery.error)}
            </SliceNotice>
          ) : directoryQuery.isLoading ? (
            <div className="space-y-2">
              <div className="h-4 w-1/2 animate-pulse rounded bg-slate-200" />
              <div className="h-4 w-2/3 animate-pulse rounded bg-slate-200" />
            </div>
          ) : directoryQuery.isError ? (
            <SliceNotice title="Could not browse slice projection" tone="error">
              {getErrorMessage(directoryQuery.error)}
            </SliceNotice>
          ) : directoryQuery.data?.entries?.length ? (
            <div className="overflow-hidden rounded-lg border border-slate-200">
              <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
                <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
                  <tr>
                    <th className="px-4 py-3">Name</th>
                    <th className="px-4 py-3">Kind</th>
                    <th className="px-4 py-3">Size</th>
                    <th className="px-4 py-3">Content hash</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-200">
                  {directoryQuery.data.entries.map((entry) => {
                    const path = entry.path || entry.name || "";
                    const target = sourceTargetForIncludedPath(
                      path,
                      slice.ref?.account
                    );

                    return (
                      <tr key={`${entry.kind}-${path}`} className="align-top">
                        <td className="px-4 py-3 font-medium text-zinc-950">
                          <Link
                            className="underline decoration-slate-300 underline-offset-4 hover:decoration-slate-700"
                            params={{
                              account: target.account,
                              _splat: target.splat
                            }}
                            to="/source/$account/$"
                          >
                            {entry.name || path || "/"}
                          </Link>
                        </td>
                        <td className="px-4 py-3 text-slate-700">
                          {entry.kind || "unknown"}
                        </td>
                        <td className="px-4 py-3 font-mono text-xs text-slate-700">
                          {entry.size ?? ""}
                        </td>
                        <td className="max-w-xs break-all px-4 py-3 font-mono text-xs text-slate-700">
                          {entry.contentHash || ""}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          ) : (
            <SliceNotice title="No projected entries">
              The selected ref returned no root entries for this slice
              projection.
            </SliceNotice>
          )}
        </SlicePanel>
      </div>
    </section>
  );
}

function buildGitCloneHint(account?: string, slug?: string) {
  const gitEnv = import.meta.env as GitCloneEnv;
  const baseUrl = (gitEnv.VITE_GITSLICE_GIT_HTTP_BASE_URL ?? "").replace(
    /\/+$/,
    ""
  );
  const clonePath = `/git/${encodeURIComponent(
    account || "account"
  )}/${encodeURIComponent(slug || "slice")}.git`;

  return {
    configured: Boolean(baseUrl),
    url: baseUrl
      ? `${baseUrl}${clonePath}`
      : `http://<git-http-host>${clonePath}`
  };
}
