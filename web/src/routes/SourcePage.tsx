import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import type {
  ListSlicesResponse,
  Slice,
  TreeEntry
} from "../api/types";
import { type ApiClient, useApi } from "../api/useApi";
import { SourceBreadcrumb } from "../components/source/SourceBreadcrumb";
import { SourceCodeViewer } from "../components/source/SourceCodeViewer";
import { SourceCoverage } from "../components/source/SourceCoverage";
import { SourceDirectoryTable } from "../components/source/SourceDirectoryTable";
import {
  buildRepositoryPath,
  decodeBase64File,
  entryKindLabel,
  isPathNotFoundError,
  isSliceProjectionDirectoryPath,
  listDirectoryAll,
  repositoryPathToRoutePath,
  routeSplat,
  searchString,
  syntheticDirectoryEntry,
  type SourceRouteParams,
  type SourceSearchParams
} from "../components/source/sourceUtils";
import { GLOBAL_REF_NAME } from "../lib/globalRef";
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
  const commitQueryParam = searchString(search.commit);
  const linkSearch: Record<string, string> = commitQueryParam
    ? { commit: commitQueryParam }
    : {};

  const latestQuery = useQuery({
    enabled: !commitQueryParam,
    queryKey: ["source", "latest", GLOBAL_REF_NAME],
    queryFn: () => resolveLatestCommit(api)
  });

  const slicesQuery = useQuery({
    enabled: Boolean(account),
    queryKey: ["source", "slices", account],
    queryFn: () => listAllSlices(api, account)
  });

  const commitId = commitQueryParam || latestQuery.data?.commitId || "";
  const isAccountRoot = Boolean(
    account && repositoryPath === buildRepositoryPath(account, "")
  );

  const pathQuery = useQuery({
    enabled: Boolean(account && commitId),
    queryKey: ["source", "path", commitId, repositoryPath],
    queryFn: () => api.resolvePath({ commitId, path: repositoryPath })
  });

  const pathNotFound = isPathNotFoundError(pathQuery.error);
  const isKnownSliceDirectory = Boolean(
    pathNotFound &&
      slicesQuery.data &&
      isSliceProjectionDirectoryPath(
        repositoryPath,
        (slicesQuery.data ?? []).flatMap(
          (slice) => slice.definition?.includedPaths ?? []
        )
      )
  );
  const isResolvingProjectedDirectory = Boolean(
    pathNotFound && slicesQuery.isPending && !slicesQuery.error
  );
  const isSyntheticDirectory =
    pathNotFound && (isAccountRoot || isKnownSliceDirectory);
  const entry =
    pathQuery.data?.entry ??
    (isSyntheticDirectory
      ? syntheticDirectoryEntry(repositoryPath, account)
      : undefined);
  const isDirectory = entry?.kind === "ENTRY_KIND_DIRECTORY";
  const isFile = entry?.kind === "ENTRY_KIND_FILE";
  const pathError =
    isSyntheticDirectory || isResolvingProjectedDirectory ? null : pathQuery.error;
  const isPathLoading = pathQuery.isPending || isResolvingProjectedDirectory;

  const directoryQuery = useQuery({
    enabled: Boolean(commitId && isDirectory),
    queryKey: ["source", "directory", commitId, repositoryPath],
    queryFn: () =>
      listDirectoryAll(api, {
        allowMissingDirectory: isSyntheticDirectory,
        commitId,
        path: repositoryPath
      })
  });

  const fileQuery = useQuery({
    enabled: Boolean(commitId && isFile),
    queryKey: ["source", "file", commitId, repositoryPath],
    queryFn: () => api.readFile({ commitId, path: repositoryPath })
  });

  function navigateSourceSearch(nextSearch: Record<string, string>) {
    if (!account) {
      return;
    }

    const routePath = repositoryPathToRoutePath(account, repositoryPath);

    void navigate({
      params: routePath
        ? ({ account, _splat: routePath } as never)
        : ({ account } as never),
      search: nextSearch as never,
      to: routePath ? "/source/$account/$" : "/source/$account"
    });
  }

  function pinCurrentCommit() {
    if (!commitId) {
      return;
    }
    navigateSourceSearch({ commit: commitId });
  }

  function viewLatest() {
    navigateSourceSearch({});
  }

  return (
    <section className="mx-auto grid w-full max-w-7xl gap-5">
      <div className="rounded-lg border border-slate-200 bg-white p-4 md:p-5">
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
        {account ? (
          <SourceMeta
            canPin={Boolean(commitId && !commitQueryParam)}
            commitId={commitId}
            isPinned={Boolean(commitQueryParam)}
            onPinCurrent={pinCurrentCommit}
            onViewLatest={viewLatest}
          />
        ) : null}
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
          isPathLoading={isPathLoading}
          linkSearch={linkSearch}
          pathError={pathError}
          account={account}
        />
      ) : latestQuery.isPending ? (
        <SourceSkeleton />
      ) : latestQuery.error ? (
        <ErrorPanel title="Unable to resolve latest source" error={latestQuery.error} />
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
          isPathLoading={isPathLoading}
          linkSearch={linkSearch}
          pathError={pathError}
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
    return <EmptyPanel message="The latest global state did not return a commit id." />;
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
  canPin: boolean;
  commitId: string;
  isPinned: boolean;
  onPinCurrent(): void;
  onViewLatest(): void;
}

function SourceMeta({
  canPin,
  commitId,
  isPinned,
  onPinCurrent,
  onViewLatest
}: SourceMetaProps) {
  return (
    <div className="mt-4 flex flex-col gap-3 border-t border-slate-200 pt-4 sm:flex-row sm:items-center sm:justify-between">
      <div className="min-w-0 text-xs text-slate-500">
        <span className="font-medium text-slate-700">
          {isPinned ? "Viewing pinned commit" : "Viewing latest global state"}
        </span>
        <span className="mt-1 block min-w-0">
          Commit:{" "}
          <span className="break-all font-mono text-slate-700">
            {commitId || "pending"}
          </span>
        </span>
      </div>
      {isPinned ? (
        <button
          className="w-fit rounded-md border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 transition hover:border-zinc-500 active:translate-y-px"
          onClick={onViewLatest}
          type="button"
        >
          View Latest
        </button>
      ) : (
        <button
          className="w-fit rounded-md border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 transition hover:border-zinc-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
          disabled={!canPin}
          onClick={onPinCurrent}
          type="button"
        >
          Pin Commit
        </button>
      )}
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

async function resolveLatestCommit(api: ApiClient) {
  const ref = await api.getRef({ refName: GLOBAL_REF_NAME });
  if (!ref.commitId) {
    throw new Error("Latest global state did not return a commit id.");
  }
  return ref;
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
