import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import type {
  ListDirectoryResponse,
  ListSlicesResponse,
  Slice,
  TreeEntry
} from "../api/types";
import { type ApiClient, useApi } from "../api/useApi";
import { SourceBreadcrumb } from "../components/source/SourceBreadcrumb";
import { SourceCodeViewer } from "../components/source/SourceCodeViewer";
import { SourceCoverage } from "../components/source/SourceCoverage";
import { SourceDirectoryTable } from "../components/source/SourceDirectoryTable";
import { SourceRefInput } from "../components/source/SourceRefInput";
import {
  DEFAULT_SOURCE_REF,
  buildRepositoryPath,
  decodeBase64File,
  entryKindLabel,
  isCommitLike,
  refNameCandidates,
  repositoryPathToRoutePath,
  routeSplat,
  searchString,
  type SourceRouteParams,
  type SourceSearchParams
} from "../components/source/sourceUtils";
import { useSelection } from "../state/selection";

export function SourcePage() {
  const api = useApi();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as SourceRouteParams;
  const search = useSearch({ strict: false }) as SourceSearchParams;
  const selection = useSelection();

  const routeAccount = params.account?.trim() ?? "";
  const account = routeAccount || selection.account.trim();
  const currentRoutePath = routeSplat(params);
  const repositoryPath = buildRepositoryPath(account, currentRoutePath);
  const refName = searchString(search.ref) || selection.ref || DEFAULT_SOURCE_REF;
  const commitQueryParam = searchString(search.commit);
  const linkSearch = commitQueryParam
    ? { commit: commitQueryParam, ref: refName }
    : { ref: refName };

  const refQuery = useQuery({
    enabled: !commitQueryParam && Boolean(refName),
    queryKey: ["source", "ref", refName],
    queryFn: () => resolveRef(api, refName)
  });

  const slicesQuery = useQuery({
    enabled: Boolean(account),
    queryKey: ["source", "slices", account],
    queryFn: () => listAllSlices(api, account)
  });

  const commitId = commitQueryParam || refQuery.data?.ref.commitId || "";

  const pathQuery = useQuery({
    enabled: Boolean(account && commitId),
    queryKey: ["source", "path", commitId, repositoryPath],
    queryFn: () => api.resolvePath({ commitId, path: repositoryPath })
  });

  const entry = pathQuery.data?.entry;
  const isDirectory = entry?.kind === "ENTRY_KIND_DIRECTORY";
  const isFile = entry?.kind === "ENTRY_KIND_FILE";

  const directoryQuery = useQuery({
    enabled: Boolean(commitId && isDirectory),
    queryKey: ["source", "directory", commitId, repositoryPath],
    queryFn: () => listDirectoryAll(api, commitId, repositoryPath)
  });

  const fileQuery = useQuery({
    enabled: Boolean(commitId && isFile),
    queryKey: ["source", "file", commitId, repositoryPath],
    queryFn: () => api.readFile({ commitId, path: repositoryPath })
  });

  function navigateToCurrentPath(value: string) {
    const trimmed = value.trim();
    if (!trimmed || !account) {
      return;
    }

    const nextSearch = isCommitLike(trimmed)
      ? { commit: trimmed, ref: refName }
      : { ref: trimmed };
    const routePath = repositoryPathToRoutePath(account, repositoryPath);

    void navigate({
      params: routePath
        ? ({ account, _splat: routePath } as never)
        : ({ account } as never),
      search: nextSearch as never,
      to: routePath ? "/source/$account/$" : "/source/$account"
    });
  }

  return (
    <section className="mx-auto grid w-full max-w-7xl gap-5">
      <div className="rounded-lg border border-slate-200 bg-white p-4 md:p-5">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
          <div className="min-w-0">
            <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
              Source Browser
            </p>
            {account ? (
              <div className="mt-2">
                <SourceBreadcrumb
                  account={account}
                  repositoryPath={repositoryPath}
                  search={linkSearch}
                />
              </div>
            ) : (
              <h1 className="mt-2 text-2xl font-semibold tracking-normal text-zinc-950">
                Select an account
              </h1>
            )}
          </div>
          <SourceRefInput
            disabled={!account}
            isCommitMode={Boolean(commitQueryParam)}
            onSubmit={navigateToCurrentPath}
            value={commitQueryParam || refName}
          />
        </div>
        <SourceMeta
          commitId={commitId}
          isCommitMode={Boolean(commitQueryParam)}
          refName={refName}
          resolvedRef={refQuery.data?.ref.name}
        />
      </div>

      {!account ? (
        <EmptyPanel message="Enter an account in the top bar or open /source/{account}." />
      ) : commitQueryParam ? (
        <SourceBody
          commitId={commitId}
          directoryEntries={directoryQuery.data ?? []}
          directoryError={directoryQuery.error}
          entry={entry}
          fileContent={decodeBase64File(fileQuery.data?.data)}
          fileError={fileQuery.error}
          isDirectoryLoading={directoryQuery.isPending}
          isFileLoading={fileQuery.isPending}
          isPathLoading={pathQuery.isPending}
          linkSearch={linkSearch}
          pathError={pathQuery.error}
          account={account}
        />
      ) : refQuery.isPending ? (
        <SourceSkeleton />
      ) : refQuery.error ? (
        <ErrorPanel title="Unable to resolve ref" error={refQuery.error} />
      ) : (
        <SourceBody
          commitId={commitId}
          directoryEntries={directoryQuery.data ?? []}
          directoryError={directoryQuery.error}
          entry={entry}
          fileContent={decodeBase64File(fileQuery.data?.data)}
          fileError={fileQuery.error}
          isDirectoryLoading={directoryQuery.isPending}
          isFileLoading={fileQuery.isPending}
          isPathLoading={pathQuery.isPending}
          linkSearch={linkSearch}
          pathError={pathQuery.error}
          account={account}
        />
      )}

      {account ? (
        <SourceCoverage
          error={slicesQuery.error}
          isLoading={slicesQuery.isPending}
          repositoryPath={repositoryPath}
          slices={slicesQuery.data ?? []}
        />
      ) : null}
    </section>
  );
}

interface SourceBodyProps {
  account: string;
  commitId: string;
  directoryEntries: TreeEntry[];
  directoryError: Error | null;
  entry: TreeEntry | undefined;
  fileContent: string;
  fileError: Error | null;
  isDirectoryLoading: boolean;
  isFileLoading: boolean;
  isPathLoading: boolean;
  linkSearch: Record<string, string | undefined>;
  pathError: Error | null;
}

function SourceBody({
  account,
  commitId,
  directoryEntries,
  directoryError,
  entry,
  fileContent,
  fileError,
  isDirectoryLoading,
  isFileLoading,
  isPathLoading,
  linkSearch,
  pathError
}: SourceBodyProps) {
  if (!commitId) {
    return <EmptyPanel message="The selected ref did not return a commit id." />;
  }

  if (isPathLoading) {
    return <SourceSkeleton />;
  }

  if (pathError) {
    return <ErrorPanel title="Unable to resolve path" error={pathError} />;
  }

  if (!entry) {
    return <EmptyPanel message="No entry was returned for this path." />;
  }

  if (entry.kind === "ENTRY_KIND_DIRECTORY") {
    if (isDirectoryLoading) {
      return <SourceSkeleton />;
    }

    if (directoryError) {
      return <ErrorPanel title="Unable to list directory" error={directoryError} />;
    }

    return (
      <SourceDirectoryTable
        account={account}
        entries={directoryEntries}
        search={linkSearch}
      />
    );
  }

  if (entry.kind === "ENTRY_KIND_FILE") {
    if (isFileLoading) {
      return <SourceSkeleton />;
    }

    if (fileError) {
      return <ErrorPanel title="Unable to read file" error={fileError} />;
    }

    return <SourceCodeViewer code={fileContent} path={entry.path ?? ""} />;
  }

  return (
    <div className="rounded-lg border border-slate-200 bg-white p-5">
      <p className="text-sm font-semibold text-zinc-950">
        {entryKindLabel(entry.kind)}
      </p>
      <dl className="mt-4 grid gap-3 text-sm sm:grid-cols-2">
        {entry.path ? <MetaRow label="Path" value={entry.path} /> : null}
        {entry.symlinkTarget ? (
          <MetaRow label="Symlink target" value={entry.symlinkTarget} />
        ) : null}
        {entry.contentHash ? <MetaRow label="Content hash" value={entry.contentHash} /> : null}
        {entry.blobId ? <MetaRow label="Blob id" value={entry.blobId} /> : null}
        {entry.treeId ? <MetaRow label="Tree id" value={entry.treeId} /> : null}
      </dl>
    </div>
  );
}

interface SourceMetaProps {
  commitId: string;
  isCommitMode: boolean;
  refName: string;
  resolvedRef: string | undefined;
}

function SourceMeta({ commitId, isCommitMode, refName, resolvedRef }: SourceMetaProps) {
  return (
    <div className="mt-4 flex flex-wrap gap-x-5 gap-y-2 border-t border-slate-200 pt-4 text-xs text-slate-500">
      <span>
        Mode:{" "}
        <span className="font-medium text-slate-700">
          {isCommitMode ? "commit" : "ref"}
        </span>
      </span>
      {!isCommitMode ? (
        <span>
          Ref: <span className="font-mono text-slate-700">{resolvedRef || refName}</span>
        </span>
      ) : null}
      <span className="min-w-0">
        Commit:{" "}
        <span className="break-all font-mono text-slate-700">
          {commitId || "pending"}
        </span>
      </span>
    </div>
  );
}

interface MetaRowProps {
  label: string;
  value: string;
}

function MetaRow({ label, value }: MetaRowProps) {
  return (
    <div>
      <dt className="text-xs font-semibold uppercase tracking-normal text-slate-500">
        {label}
      </dt>
      <dd className="mt-1 break-all font-mono text-sm text-slate-700">{value}</dd>
    </div>
  );
}

function SourceSkeleton() {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-5">
      <div className="grid gap-3">
        <div className="h-5 w-2/5 animate-pulse rounded bg-slate-200" />
        <div className="h-12 animate-pulse rounded bg-slate-100" />
        <div className="h-12 animate-pulse rounded bg-slate-100" />
        <div className="h-12 animate-pulse rounded bg-slate-100" />
      </div>
    </div>
  );
}

interface EmptyPanelProps {
  message: string;
}

function EmptyPanel({ message }: EmptyPanelProps) {
  return (
    <div className="rounded-lg border border-dashed border-slate-300 bg-white p-8 text-sm text-slate-600">
      {message}
    </div>
  );
}

interface ErrorPanelProps {
  error: Error;
  title: string;
}

function ErrorPanel({ error, title }: ErrorPanelProps) {
  return (
    <div className="rounded-lg border border-rose-200 bg-rose-50 p-5 text-sm">
      <p className="font-semibold text-rose-950">{title}</p>
      <p className="mt-2 text-rose-800">{error.message}</p>
    </div>
  );
}

async function resolveRef(api: ApiClient, refName: string) {
  const candidates = refNameCandidates(refName);
  let lastError: unknown;

  for (const candidate of candidates) {
    try {
      const commit = await api.getRef({ refName: candidate });
      if (!commit.commitId) {
        throw new Error(`Ref ${candidate} did not return a commit id`);
      }
      return { ref: commit, requestedRefName: refName };
    } catch (error) {
      lastError = error;
    }
  }

  throw lastError instanceof Error
    ? lastError
    : new Error(`Unable to resolve ref ${refName}`);
}

async function listDirectoryAll(
  api: ApiClient,
  commitId: string,
  path: string
) {
  const entries: TreeEntry[] = [];
  let cursor = "";

  do {
    const page: ListDirectoryResponse = await api.listDirectory({
      commitId,
      cursor,
      pageSize: 500,
      path
    });
    entries.push(...(page.entries ?? []));
    cursor = page.nextCursor ?? "";
  } while (cursor);

  return entries;
}

async function listAllSlices(api: ApiClient, account: string) {
  const slices: Slice[] = [];
  let cursor = "";

  do {
    const page: ListSlicesResponse = await api.listSlices({
      account,
      cursor,
      pageSize: 500
    });
    slices.push(...(page.slices ?? []));
    cursor = page.nextCursor ?? "";
  } while (cursor);

  return slices;
}
