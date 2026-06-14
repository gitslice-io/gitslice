import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQueries, useQuery } from "@tanstack/react-query";

import type {
  SliceRef,
  TreeEntry
} from "../api/types";
import { type ApiClient, useApi } from "../api/useApi";
import { SourceCodeViewer } from "../components/source/SourceCodeViewer";
import {
  DraftChangesetPanel,
  DirectoryCreateControls,
  InlineRenameForm,
  joinRepositoryPath,
  parentRepositoryPath as editingParentRepositoryPath,
  pendingChildrenForDirectory,
  pendingDeleteForPath,
  pendingEditKey,
  pendingRenameForPath,
  pendingWriteForPath,
  repositoryPathName as editingRepositoryPathName,
  useDraftChangesetController,
  validateEntryName,
  type PendingEdit
} from "../components/source/SliceEditing";
import {
  decodeBase64File,
  entryDisplayName,
  entryKindLabel,
  formatSize,
  isSliceProjectionDirectoryPath,
  listDirectoryAll,
  normalizeRepositoryPath,
  sortEntries,
  syntheticDirectoryEntry
} from "../components/source/sourceUtils";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePageHeader,
  SlicePanel,
  getErrorMessage,
  sliceDisplayName
} from "../components/slices/SlicePageParts";
import { GLOBAL_REF_NAME } from "../lib/globalRef";
import { useSelection } from "../state/selection";

interface SliceParams {
  id?: string;
}

interface SliceSearch {
  path?: unknown;
}

interface GitCloneEnv extends ImportMetaEnv {
  readonly VITE_GITSLICE_GIT_HTTP_BASE_URL?: string;
}

export function SliceDetailPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { subjectId } = useSelection();
  const params = useParams({ strict: false }) as SliceParams;
  const search = useSearch({ strict: false }) as SliceSearch;
  const sliceId = params.id ?? "";
  const selectedPath = pathSearchValue(search.path);

  const sliceQuery = useQuery({
    enabled: sliceId.length > 0,
    queryKey: ["slice", sliceId],
    queryFn: () => api.getSlice({ sliceId })
  });

  const latestQuery = useQuery({
    queryKey: ["globalRef", GLOBAL_REF_NAME],
    queryFn: async () => {
      const ref = await api.getRef({ refName: GLOBAL_REF_NAME });
      if (!ref.commitId) {
        throw new Error("Latest global state did not return a commit id.");
      }
      return ref;
    }
  });

  const slice = sliceQuery.data;
  const sliceRef = slice?.ref;
  const sliceLabel = sliceDisplayName(slice);
  const commitId = latestQuery.data?.commitId ?? "";
  const draftChangeset = useDraftChangesetController({
    api,
    commitId,
    sliceLabel,
    sliceRef,
    subjectId
  });
  const pendingEdits = draftChangeset.edits;
  const isProjectedDirectoryPath = isSliceProjectionDirectoryPath(
    selectedPath,
    slice?.definition?.includedPaths ?? []
  );

  const pathQuery = useQuery({
    enabled: Boolean(commitId && selectedPath && !isProjectedDirectoryPath),
    queryKey: ["slicePath", sliceId, commitId, selectedPath],
    queryFn: () => api.resolvePath({ commitId, path: selectedPath })
  });

  const entry = isProjectedDirectoryPath
    ? syntheticDirectoryEntry(selectedPath)
    : pathQuery.data?.entry;
  const isDirectory = entry?.kind === "ENTRY_KIND_DIRECTORY";
  const isFile = entry?.kind === "ENTRY_KIND_FILE";

  const directoryQuery = useQuery({
    enabled: Boolean(commitId && sliceRef?.account && sliceRef?.slice && isDirectory),
    queryKey: [
      "sliceDirectory",
      sliceId,
      commitId,
      selectedPath,
      sliceRef?.account,
      sliceRef?.slice
    ],
    queryFn: () =>
      listDirectoryAll(api, {
        allowMissingDirectory: isProjectedDirectoryPath,
        commitId,
        path: selectedPath,
        slice: sliceRef
      })
  });

  const fileQuery = useQuery({
    enabled: Boolean(commitId && selectedPath && isFile),
    queryKey: ["sliceFile", sliceId, commitId, selectedPath],
    queryFn: () => api.readFile({ commitId, path: selectedPath })
  });

  function selectPath(path: string) {
    void navigate({
      params: { id: sliceId } as never,
      search: path ? ({ path } as never) : ({} as never),
      to: "/slices/$id"
    });
  }

  function stagePendingEdit(edit: PendingEdit) {
    draftChangeset.stageEdit(edit);
  }

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
        <SlicePageHeader title="Slice Home" />
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
        <SlicePageHeader title="Slice Home" />
        <div className="mt-8">
          <SliceNotice title="Slice not found">
            No slice was returned for id {sliceId || "unknown"}.
          </SliceNotice>
        </div>
      </section>
    );
  }

  const includedPaths = slice.definition?.includedPaths ?? [];
  const gitCloneHint = buildGitCloneHint(slice.ref?.account, slice.ref?.slice);
  const currentEntries = directoryQuery.data ?? [];
  // The directory new file/folder controls create under the current folder. At
  // the slice root selectedPath is "" — which would build account-only paths the
  // server rejects (paths need at least an account + one more segment), so fall
  // back to the slice's included root when there is exactly one.
  const projectedRoots = includedPaths
    .map((includedPath) => normalizeTreePath(includedPath))
    .filter(Boolean);
  const createDirectory =
    selectedPath || (projectedRoots.length === 1 ? projectedRoots[0] : "");

  return (
    <section className="mx-auto w-full max-w-7xl">
      <SlicePageHeader
        actions={
          <div className="flex flex-wrap gap-2">
            <Link
              className="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
              search={{ slice: sliceLabel } as never}
              to="/changesets"
            >
              Changesets
            </Link>
            <Link
              className="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
              params={{ id: sliceId }}
              to="/slices/$id/settings"
            >
              Settings
            </Link>
            <GitCloneDropdown cloneUrl={gitCloneHint.url} />
          </div>
        }
        title={sliceLabel}
        description="Slice home with projected source navigation and in-place draft changeset editing."
      />

      <div className="mt-8 grid gap-6 lg:grid-cols-[19rem_minmax(0,1fr)]">
        <aside className="lg:sticky lg:top-24 lg:self-start">
          <SliceFolderNavigator
            api={api}
            commitId={commitId}
            includedPaths={includedPaths}
            isLatestLoading={latestQuery.isPending}
            isSelectedDirectory={isDirectory}
            onSelectPath={selectPath}
            selectedPath={selectedPath}
            sliceId={sliceId}
            sliceRef={sliceRef}
          />
        </aside>

        <div className="min-w-0 space-y-6">
          <DraftChangesetPanel {...draftChangeset} />
          <SliceSourceWorkspace
            commitError={latestQuery.error}
            commitId={commitId}
            createDirectory={createDirectory}
            directoryEntries={currentEntries}
            directoryError={directoryQuery.error}
            entry={entry}
            fileContent={decodeBase64File(fileQuery.data?.data)}
            fileError={fileQuery.error}
            isDirectoryLoading={directoryQuery.isPending}
            isFileLoading={fileQuery.isPending}
            isLatestLoading={latestQuery.isPending}
            isPathLoading={pathQuery.isLoading}
            onSelectPath={selectPath}
            onStageEdit={stagePendingEdit}
            pathError={pathQuery.error}
            pendingEdits={pendingEdits}
            selectedPath={selectedPath}
          />
        </div>
      </div>
    </section>
  );
}

function GitCloneDropdown({ cloneUrl }: { cloneUrl: string }) {
  return (
    <details className="group relative">
      <summary className="flex cursor-pointer list-none items-center rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]">
        Clone
      </summary>
      <div className="absolute right-0 mt-2 w-[min(24rem,calc(100vw-2rem))] rounded-md border border-slate-200 bg-white p-3 shadow-lg shadow-slate-900/10">
        <label
          className="block text-xs font-semibold uppercase tracking-normal text-slate-500"
          htmlFor="git-clone-url"
        >
          Git endpoint
        </label>
        <div className="mt-2 flex min-w-0 items-stretch gap-2">
          <input
            className="min-w-0 flex-1 rounded-md border border-slate-300 bg-slate-50 px-2.5 py-2 font-mono text-xs text-zinc-950"
            id="git-clone-url"
            readOnly
            value={cloneUrl}
          />
          <button
            className="rounded-md bg-zinc-950 px-3 py-2 text-xs font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
            onClick={() => void navigator.clipboard?.writeText(cloneUrl)}
            type="button"
          >
            Copy
          </button>
        </div>
      </div>
    </details>
  );
}

interface SliceFolderNavigatorProps {
  api: ApiClient;
  commitId: string;
  includedPaths: string[];
  isLatestLoading: boolean;
  isSelectedDirectory: boolean;
  onSelectPath(path: string): void;
  selectedPath: string;
  sliceId: string;
  sliceRef: SliceRef | undefined;
}

function SliceFolderNavigator({
  api,
  commitId,
  includedPaths,
  isLatestLoading,
  isSelectedDirectory,
  onSelectPath,
  selectedPath,
  sliceId,
  sliceRef
}: SliceFolderNavigatorProps) {
  const initialExpandedPaths = useMemo(
    () => initialTreeExpansion(selectedPath, includedPaths, isSelectedDirectory),
    [includedPaths, isSelectedDirectory, selectedPath]
  );
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(
    () => initialExpandedPaths
  );

  useEffect(() => {
    setExpandedPaths((current) => {
      const next = new Set(current);
      const selectedExpansion = isSelectedDirectory
        ? ancestorDirectoryPaths(selectedPath)
        : parentAncestorDirectoryPaths(selectedPath);
      for (const path of selectedExpansion) {
        next.add(path);
      }
      return next;
    });
  }, [isSelectedDirectory, selectedPath]);

  useEffect(() => {
    setExpandedPaths((current) => {
      const next = new Set(current);
      for (const path of includedPaths.flatMap((includedPath) =>
        ancestorDirectoryPaths(normalizeTreePath(includedPath))
      )) {
        next.add(path);
      }
      return next;
    });
  }, [includedPaths]);

  const expandedDirectoryPaths = useMemo(
    () => Array.from(expandedPaths).sort(compareRepositoryPaths),
    [expandedPaths]
  );

  const directoryQueries = useQueries({
    queries: expandedDirectoryPaths.map((path) => ({
      enabled: Boolean(
        commitId && sliceRef?.account && sliceRef?.slice && !isLatestLoading
      ),
      queryKey: [
        "sliceTreeDirectory",
        sliceId,
        commitId,
        path,
        sliceRef?.account,
        sliceRef?.slice
      ],
      queryFn: () =>
        listDirectoryAll(api, {
          allowMissingDirectory: isSliceProjectionDirectoryPath(
            path,
            includedPaths
          ),
          commitId,
          path,
          slice: sliceRef
        })
    }))
  });

  const directoryResults = useMemo(
    () =>
      expandedDirectoryPaths.map((path, index) => ({
        data: directoryQueries[index]?.data ?? [],
        error: directoryQueries[index]?.error ?? null,
        isLoading: Boolean(directoryQueries[index]?.isPending),
        path
      })),
    [directoryQueries, expandedDirectoryPaths]
  );

  const root = useMemo(
    () => buildSourceTree(includedPaths, directoryResults),
    [directoryResults, includedPaths]
  );
  const rows = useMemo(
    () => flattenTreeRows(root, expandedPaths),
    [expandedPaths, root]
  );
  const isRootLoading = Boolean(
    directoryResults.find((result) => result.path === "")?.isLoading
  );

  function togglePath(path: string) {
    setExpandedPaths((current) => {
      const next = new Set(current);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  }

  return (
    <SlicePanel className="max-h-[calc(100dvh-8rem)] overflow-auto p-0">
      <div className="border-b border-slate-200 px-3 py-3">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-zinc-950">Files</h2>
            <p className="mt-1 truncate text-xs leading-5 text-slate-500">
              Slice-projected source tree
            </p>
          </div>
          <button
            className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
            onClick={() => onSelectPath("")}
            type="button"
          >
            Root
          </button>
        </div>
      </div>

      <div className="py-2">
        {isLatestLoading || (isRootLoading && !rows.length) ? (
          <div className="px-3 py-2">
            <NavigatorSkeleton />
          </div>
        ) : rows.length ? (
          rows.map((row) => (
            <TreeRow
              depth={row.depth}
              isExpanded={expandedPaths.has(row.node.path)}
              isLoading={Boolean(
                directoryResults.find((result) => result.path === row.node.path)
                  ?.isLoading
              )}
              key={row.node.path || "root-row"}
              node={row.node}
              onSelect={onSelectPath}
              onToggle={togglePath}
              selectedPath={selectedPath}
            />
          ))
        ) : (
          <p className="mx-3 rounded-md border border-dashed border-slate-300 p-3 text-sm text-slate-600">
            No files in this slice.
          </p>
        )}

        {directoryResults
          .filter((result) => result.error)
          .map((result) => (
            <p
              className="mx-3 mt-2 rounded-md bg-rose-50 p-3 text-sm text-rose-800"
              key={`error-${result.path || "root"}`}
            >
              Could not load {result.path || "slice root"}:{" "}
              {result.error?.message}
            </p>
          ))}
      </div>
    </SlicePanel>
  );
}

interface DirectoryLoadResult {
  data: TreeEntry[];
  error: Error | null;
  isLoading: boolean;
  path: string;
}

interface SourceTreeNode {
  children: Map<string, SourceTreeNode>;
  kind: TreeEntry["kind"];
  name: string;
  path: string;
  synthetic: boolean;
}

interface SourceTreeRow {
  depth: number;
  node: SourceTreeNode;
}

function TreeRow({
  depth,
  isExpanded,
  isLoading,
  node,
  onSelect,
  onToggle,
  selectedPath
}: {
  depth: number;
  isExpanded: boolean;
  isLoading: boolean;
  node: SourceTreeNode;
  onSelect(path: string): void;
  onToggle(path: string): void;
  selectedPath: string;
}) {
  const isDirectory = node.kind === "ENTRY_KIND_DIRECTORY";
  const isActive = selectedPath === node.path;
  const buttonLabel = node.path || "slice root";

  return (
    <div>
      <div
        className={[
          "group flex h-8 min-w-0 items-center gap-1.5 pr-2 text-sm transition",
          isActive
            ? "bg-slate-100 text-zinc-950"
            : "text-slate-700 hover:bg-slate-50 hover:text-zinc-950"
        ].join(" ")}
        style={{ paddingLeft: `${depth * 14 + 8}px` }}
      >
        {isDirectory ? (
          <button
            aria-expanded={isExpanded}
            aria-label={`${isExpanded ? "Collapse" : "Expand"} ${buttonLabel}`}
            className="flex h-5 w-5 shrink-0 items-center justify-center rounded text-slate-500 transition hover:bg-slate-200 hover:text-zinc-950"
            onClick={() => onToggle(node.path)}
            type="button"
          >
            <span
              className={[
                "h-0 w-0 border-y-[4px] border-l-[5px] border-y-transparent border-l-current transition-transform",
                isExpanded ? "rotate-90" : ""
              ].join(" ")}
            />
          </button>
        ) : (
          <span className="h-5 w-5 shrink-0" />
        )}

        <button
          className="flex min-w-0 flex-1 items-center gap-2 py-1 text-left"
          onClick={() => onSelect(node.path)}
          title={buttonLabel}
          type="button"
        >
          {isDirectory ? <FolderGlyph /> : <FileGlyph />}
          <span
            className={[
              "truncate",
              isActive ? "font-semibold" : "font-medium",
              node.synthetic ? "text-slate-600" : ""
            ].join(" ")}
          >
            {node.name}
          </span>
        </button>

        {isLoading ? (
          <span className="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-slate-400" />
        ) : null}
      </div>
    </div>
  );
}

function FolderGlyph() {
  return (
    <span
      aria-hidden="true"
      className="relative h-3.5 w-4 shrink-0 rounded-[2px] border border-amber-400 bg-amber-100 before:absolute before:-top-1 before:left-0.5 before:h-1 before:w-2 before:rounded-t-[2px] before:border before:border-b-0 before:border-amber-400 before:bg-amber-100"
    />
  );
}

function FileGlyph() {
  return (
    <span
      aria-hidden="true"
      className="relative h-4 w-3 shrink-0 rounded-[2px] border border-slate-300 bg-white before:absolute before:right-0 before:top-0 before:h-1.5 before:w-1.5 before:border-b before:border-l before:border-slate-200 before:bg-slate-50"
    />
  );
}

function buildSourceTree(
  includedPaths: string[],
  directoryResults: DirectoryLoadResult[]
) {
  const root: SourceTreeNode = {
    children: new Map(),
    kind: "ENTRY_KIND_DIRECTORY",
    name: "slice root",
    path: "",
    synthetic: false
  };
  const nodes = new Map<string, SourceTreeNode>([["", root]]);

  function ensureNode(
    rawPath: string,
    kind: TreeEntry["kind"] = "ENTRY_KIND_DIRECTORY",
    name?: string,
    synthetic = true
  ) {
    const path = normalizeTreePath(rawPath);
    if (!path) {
      return root;
    }

    const parentPath = parentRepositoryPath(path);
    const parent = ensureNode(parentPath);
    let node = nodes.get(path);
    if (!node) {
      node = {
        children: new Map(),
        kind,
        name: name ?? repositoryPathName(path),
        path,
        synthetic
      };
      nodes.set(path, node);
      parent.children.set(path, node);
    } else {
      if (kind) {
        node.kind = kind;
      }
      node.name = name ?? node.name;
      node.synthetic = node.synthetic && synthetic;
    }
    return node;
  }

  for (const includedPath of includedPaths) {
    for (const path of ancestorDirectoryPaths(normalizeTreePath(includedPath))) {
      ensureNode(path);
    }
    ensureNode(includedPath);
  }

  for (const result of directoryResults) {
    ensureNode(result.path);
    for (const entry of result.data) {
      const path = normalizeTreePath(entry.path ?? "");
      if (!path) {
        continue;
      }
      ensureNode(path, entry.kind, entryDisplayName(entry), false);
    }
  }

  return root;
}

function flattenTreeRows(
  root: SourceTreeNode,
  expandedPaths: Set<string>
): SourceTreeRow[] {
  if (!expandedPaths.has("")) {
    return [];
  }

  const rows: SourceTreeRow[] = [];

  function visit(node: SourceTreeNode, depth: number) {
    const children = Array.from(node.children.values()).sort(compareTreeNodes);
    for (const child of children) {
      rows.push({ depth, node: child });
      if (child.kind === "ENTRY_KIND_DIRECTORY" && expandedPaths.has(child.path)) {
        visit(child, depth + 1);
      }
    }
  }

  visit(root, 0);
  return rows;
}

function initialTreeExpansion(
  selectedPath: string,
  includedPaths: string[],
  isSelectedDirectory: boolean
) {
  const paths = new Set<string>([""]);
  const selectedExpansion = isSelectedDirectory
    ? ancestorDirectoryPaths(selectedPath)
    : parentAncestorDirectoryPaths(selectedPath);
  for (const path of selectedExpansion) {
    paths.add(path);
  }
  for (const includedPath of includedPaths) {
    for (const path of ancestorDirectoryPaths(normalizeTreePath(includedPath))) {
      paths.add(path);
    }
  }
  return paths;
}

function parentAncestorDirectoryPaths(path: string) {
  return ancestorDirectoryPaths(parentRepositoryPath(path));
}

function ancestorDirectoryPaths(path: string) {
  const normalized = normalizeTreePath(path);
  const parts = normalized.split("/").filter(Boolean);
  const paths = [""];

  for (let index = 1; index < parts.length; index += 1) {
    paths.push(`/${parts.slice(0, index).join("/")}`);
  }

  if (normalized && !paths.includes(normalized)) {
    paths.push(normalized);
  }

  return paths;
}

function parentRepositoryPath(path: string) {
  const parts = normalizeTreePath(path).split("/").filter(Boolean);
  if (parts.length <= 1) {
    return "";
  }
  return `/${parts.slice(0, -1).join("/")}`;
}

function repositoryPathName(path: string) {
  const parts = normalizeTreePath(path).split("/").filter(Boolean);
  return parts[parts.length - 1] ?? "slice root";
}

function normalizeTreePath(path: string) {
  const normalized = normalizeRepositoryPath(path);
  return normalized === "/" ? "" : normalized;
}

function compareTreeNodes(left: SourceTreeNode, right: SourceTreeNode) {
  if (left.kind !== right.kind) {
    if (left.kind === "ENTRY_KIND_DIRECTORY") return -1;
    if (right.kind === "ENTRY_KIND_DIRECTORY") return 1;
  }
  return left.name.localeCompare(right.name, undefined, {
    numeric: true,
    sensitivity: "base"
  });
}

function compareRepositoryPaths(left: string, right: string) {
  return left.localeCompare(right, undefined, {
    numeric: true,
    sensitivity: "base"
  });
}

interface SliceSourceWorkspaceProps {
  commitError: Error | null;
  commitId: string;
  directoryEntries: TreeEntry[];
  directoryError: Error | null;
  entry: TreeEntry | undefined;
  fileContent: string;
  fileError: Error | null;
  isDirectoryLoading: boolean;
  isFileLoading: boolean;
  isLatestLoading: boolean;
  isPathLoading: boolean;
  onSelectPath(path: string): void;
  onStageEdit(edit: PendingEdit): void;
  pathError: Error | null;
  pendingEdits: PendingEdit[];
  selectedPath: string;
  createDirectory: string;
}

function SliceSourceWorkspace({
  commitError,
  commitId,
  createDirectory,
  directoryEntries,
  directoryError,
  entry,
  fileContent,
  fileError,
  isDirectoryLoading,
  isFileLoading,
  isLatestLoading,
  isPathLoading,
  onSelectPath,
  onStageEdit,
  pathError,
  pendingEdits,
  selectedPath
}: SliceSourceWorkspaceProps) {
  if (isLatestLoading || isPathLoading) {
    return <SourceSkeleton />;
  }

  if (commitError) {
    return (
      <SliceNotice title="Could not resolve latest source" tone="error">
        {commitError.message}
      </SliceNotice>
    );
  }

  if (!commitId) {
    return (
      <SliceNotice title="Missing commit">
        The latest global state did not return a commit id.
      </SliceNotice>
    );
  }

  if (pathError) {
    return (
      <SliceNotice title="Could not resolve path" tone="error">
        {pathError.message}
      </SliceNotice>
    );
  }

  if (!entry) {
    return (
      <SliceNotice title="Path not found">
        No source entry was returned for {selectedPath || "slice root"}.
      </SliceNotice>
    );
  }

  if (entry.kind === "ENTRY_KIND_DIRECTORY") {
    if (isDirectoryLoading) {
      return <SourceSkeleton />;
    }
    if (directoryError) {
      return (
        <SliceNotice title="Could not list directory" tone="error">
          {directoryError.message}
        </SliceNotice>
      );
    }

    return (
      <SlicePanel className="p-0">
        <DirectoryHeader
          commitId={commitId}
          onStageEdit={onStageEdit}
          selectedPath={selectedPath}
        />
        <DirectoryCreateControls
          directoryPath={createDirectory}
          onStageEdit={onStageEdit}
        />
        <SliceDirectoryTable
          entries={directoryEntries}
          onSelectPath={onSelectPath}
          onStageEdit={onStageEdit}
          pendingEdits={pendingEdits}
          selectedPath={selectedPath}
        />
      </SlicePanel>
    );
  }

  if (entry.kind === "ENTRY_KIND_FILE") {
    if (isFileLoading) {
      return <SourceSkeleton />;
    }
    if (fileError) {
      return (
        <SliceNotice title="Could not read file" tone="error">
          {fileError.message}
        </SliceNotice>
      );
    }

    return (
      <EditableFileView
        commitId={commitId}
        fileContent={fileContent}
        onStageEdit={onStageEdit}
        pendingEdits={pendingEdits}
        selectedPath={entry.path ?? selectedPath}
      />
    );
  }

  return (
    <SlicePanel>
      <DirectoryHeader commitId={commitId} selectedPath={selectedPath} />
      <p className="mt-4 text-sm text-slate-600">
        {entryKindLabel(entry.kind)} entries are visible in the navigator but do
        not have a preview in this view.
      </p>
    </SlicePanel>
  );
}

function DirectoryHeader({
  commitId,
  onStageEdit,
  selectedPath
}: {
  commitId: string;
  onStageEdit?: (edit: PendingEdit) => void;
  selectedPath: string;
}) {
  const [isRenaming, setIsRenaming] = useState(false);
  const canRename = Boolean(selectedPath && onStageEdit);

  function stageRename(name: string) {
    if (!onStageEdit) {
      return;
    }
    onStageEdit({
      kind: "rename",
      oldPath: selectedPath,
      path: joinRepositoryPath(editingParentRepositoryPath(selectedPath), name)
    });
    setIsRenaming(false);
  }

  return (
    <div className="border-b border-slate-200 px-5 py-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <h2 className="break-all text-base font-semibold text-zinc-950">
            {selectedPath || "Slice root"}
          </h2>
          <p className="mt-1 break-all text-xs text-slate-500">
            Commit <span className="font-mono text-slate-700">{commitId}</span>
          </p>
        </div>
        {canRename ? (
          <div className="flex shrink-0 flex-wrap gap-2">
            <button
              className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
              onClick={() => setIsRenaming(true)}
              type="button"
            >
              Rename
            </button>
            <button
              className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
              onClick={() => onStageEdit?.({ kind: "delete", path: selectedPath })}
              type="button"
            >
              Delete
            </button>
          </div>
        ) : null}
      </div>
      {isRenaming ? (
        <div className="mt-3">
          <InlineRenameForm
            onCancel={() => setIsRenaming(false)}
            onSave={stageRename}
            originalName={editingRepositoryPathName(selectedPath)}
          />
        </div>
      ) : null}
    </div>
  );
}

function SliceDirectoryTable({
  entries,
  onSelectPath,
  onStageEdit,
  pendingEdits,
  selectedPath
}: {
  entries: TreeEntry[];
  onSelectPath(path: string): void;
  onStageEdit(edit: PendingEdit): void;
  pendingEdits: PendingEdit[];
  selectedPath: string;
}) {
  const [renamingPath, setRenamingPath] = useState("");
  const sortedEntries = sortEntries(entries);
  const pendingChildren = pendingChildrenForDirectory(pendingEdits, selectedPath);

  if (!sortedEntries.length && !pendingChildren.length) {
    return (
      <div className="p-8 text-sm text-slate-600">
        This slice-projected directory is empty.
      </div>
    );
  }

  return (
    <div className="overflow-x-auto">
      <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
        <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
          <tr>
            <th className="px-4 py-3">Name</th>
            <th className="px-4 py-3">Kind</th>
            <th className="px-4 py-3">Size</th>
            <th className="px-4 py-3">Content hash</th>
            <th className="px-4 py-3">Actions</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100">
          {sortedEntries.map((entry) => {
            const path = normalizeRepositoryPath(entry.path ?? "");
            const isDirectory = entry.kind === "ENTRY_KIND_DIRECTORY";
            const pendingDelete = pendingDeleteForPath(pendingEdits, path);
            const pendingRename = pendingRenameForPath(pendingEdits, path);
            const pendingWrite = pendingWriteForPath(pendingEdits, path);
            const isRenaming = renamingPath === path;
            const displayName = entryDisplayName(entry);

            return (
              <tr className="align-top transition hover:bg-slate-50" key={entry.path ?? entryDisplayName(entry)}>
                <td className="min-w-56 px-4 py-3">
                  <button
                    className="text-left font-medium text-zinc-950 underline-offset-4 hover:underline"
                    onClick={() => onSelectPath(path)}
                    type="button"
                  >
                    {displayName}
                    {isDirectory ? "/" : ""}
                  </button>
                  <PendingRowBadges
                    isDeleted={Boolean(pendingDelete)}
                    isEdited={Boolean(pendingWrite)}
                    renamePath={pendingRename?.path}
                  />
                  {entry.path ? (
                    <div className="mt-1 max-w-96 truncate font-mono text-xs text-slate-400">
                      {entry.path}
                    </div>
                  ) : null}
                </td>
                <td className="px-4 py-3 text-slate-600">
                  {entryKindLabel(entry.kind)}
                </td>
                <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-slate-600">
                  {formatSize(entry.size)}
                </td>
                <td className="max-w-md break-all px-4 py-3 font-mono text-xs text-slate-600">
                  {entry.contentHash || entry.blobId || entry.treeId || ""}
                </td>
                <td className="min-w-48 px-4 py-3">
                  {isRenaming ? (
                    <InlineRenameForm
                      onCancel={() => setRenamingPath("")}
                      onSave={(name) => {
                        onStageEdit({
                          kind: "rename",
                          oldPath: path,
                          path: joinRepositoryPath(
                            editingParentRepositoryPath(path),
                            name
                          )
                        });
                        setRenamingPath("");
                      }}
                      originalName={displayName}
                    />
                  ) : (
                    <div className="flex flex-wrap gap-2">
                      <button
                        className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
                        onClick={() => setRenamingPath(path)}
                        type="button"
                      >
                        Rename
                      </button>
                      <button
                        className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
                        onClick={() => onStageEdit({ kind: "delete", path })}
                        type="button"
                      >
                        Delete
                      </button>
                    </div>
                  )}
                </td>
              </tr>
            );
          })}
          {pendingChildren.map((edit) => {
            const isDirectory = edit.kind === "mkdir";
            const path = edit.path;

            return (
              <tr
                className="align-top bg-slate-50 transition hover:bg-slate-100"
                key={pendingEditKey(edit)}
              >
                <td className="min-w-56 px-4 py-3">
                  <span className="font-medium text-zinc-950">
                    {editingRepositoryPathName(path)}
                    {isDirectory ? "/" : ""}
                  </span>
                  <div className="mt-2">
                    <span className="inline-flex rounded-md border border-slate-200 bg-white px-2 py-1 text-xs font-semibold text-slate-700">
                      pending
                    </span>
                  </div>
                  <div className="mt-1 max-w-96 truncate font-mono text-xs text-slate-400">
                    {path}
                  </div>
                </td>
                <td className="px-4 py-3 text-slate-600">
                  {isDirectory ? "directory" : "file"}
                </td>
                <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-slate-600">
                  {edit.kind === "write" && edit.content !== undefined
                    ? formatSize(String(edit.content.length))
                    : ""}
                </td>
                <td className="max-w-md break-all px-4 py-3 font-mono text-xs text-slate-600" />
                <td className="px-4 py-3 text-xs text-slate-500">
                  Remove from the pending changes panel.
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function PendingRowBadges({
  isDeleted,
  isEdited,
  renamePath
}: {
  isDeleted: boolean;
  isEdited: boolean;
  renamePath?: string;
}) {
  if (!isDeleted && !isEdited && !renamePath) {
    return null;
  }

  return (
    <div className="mt-2 flex flex-wrap gap-1.5">
      {isEdited ? <PendingBadge>pending edited</PendingBadge> : null}
      {isDeleted ? <PendingBadge>pending deleted</PendingBadge> : null}
      {renamePath ? (
        <PendingBadge>
          pending rename to {editingRepositoryPathName(renamePath)}
        </PendingBadge>
      ) : null}
    </div>
  );
}

function PendingBadge({ children }: { children: ReactNode }) {
  return (
    <span className="inline-flex rounded-md border border-slate-200 bg-slate-50 px-2 py-1 text-xs font-semibold text-slate-700">
      {children}
    </span>
  );
}

function EditableFileView({
  commitId,
  fileContent,
  onStageEdit,
  pendingEdits,
  selectedPath
}: {
  commitId: string;
  fileContent: string;
  onStageEdit(edit: PendingEdit): void;
  pendingEdits: PendingEdit[];
  selectedPath: string;
}) {
  const pendingWrite = pendingWriteForPath(pendingEdits, selectedPath);
  const pendingDelete = pendingDeleteForPath(pendingEdits, selectedPath);
  const pendingRename = pendingRenameForPath(pendingEdits, selectedPath);
  const displayedContent = pendingWrite?.content ?? fileContent;
  const [isEditing, setIsEditing] = useState(false);
  const [isRenaming, setIsRenaming] = useState(false);
  const [draft, setDraft] = useState(displayedContent);
  const [renameError, setRenameError] = useState("");

  useEffect(() => {
    setIsEditing(false);
    setIsRenaming(false);
    setRenameError("");
    setDraft(displayedContent);
  }, [displayedContent, selectedPath]);

  function saveEdit() {
    onStageEdit({
      kind: "write",
      path: selectedPath,
      content: draft,
      isNew: false
    });
    setIsEditing(false);
  }

  function saveRename(name: string) {
    const validationError = validateEntryName(name);
    if (validationError) {
      setRenameError(validationError);
      return;
    }
    onStageEdit({
      kind: "rename",
      oldPath: selectedPath,
      path: joinRepositoryPath(editingParentRepositoryPath(selectedPath), name)
    });
    setIsRenaming(false);
    setRenameError("");
  }

  return (
    <div className="space-y-3">
      <SlicePanel className="p-0">
        <DirectoryHeader commitId={commitId} selectedPath={selectedPath} />
        <div className="border-b border-slate-200 bg-slate-50 px-5 py-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                {pendingWrite ? <PendingBadge>pending</PendingBadge> : null}
                {pendingDelete ? <PendingBadge>pending deleted</PendingBadge> : null}
                {pendingRename ? (
                  <PendingBadge>
                    pending rename to{" "}
                    {editingRepositoryPathName(pendingRename.path)}
                  </PendingBadge>
                ) : null}
              </div>
            </div>
            <div className="flex shrink-0 flex-wrap gap-2">
              <button
                className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
                onClick={() => {
                  setDraft(displayedContent);
                  setIsEditing(true);
                }}
                type="button"
              >
                Edit
              </button>
              <button
                className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
                onClick={() => setIsRenaming(true)}
                type="button"
              >
                Rename
              </button>
              <button
                className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
                onClick={() => onStageEdit({ kind: "delete", path: selectedPath })}
                type="button"
              >
                Delete
              </button>
            </div>
          </div>
          {isRenaming ? (
            <div className="mt-3">
              <InlineRenameForm
                onCancel={() => {
                  setIsRenaming(false);
                  setRenameError("");
                }}
                onSave={saveRename}
                originalName={editingRepositoryPathName(selectedPath)}
              />
              {renameError ? (
                <p className="mt-2 text-sm text-rose-700">{renameError}</p>
              ) : null}
            </div>
          ) : null}
        </div>
        {isEditing ? (
          <div className="p-5">
            <label className="grid gap-2 text-sm font-medium text-zinc-800">
              File content
              <textarea
                className="min-h-[28rem] rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm leading-6 text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
                onChange={(event) => setDraft(event.target.value)}
                value={draft}
              />
            </label>
            <div className="mt-4 flex flex-wrap gap-2">
              <button
                className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
                onClick={saveEdit}
                type="button"
              >
                Save
              </button>
              <button
                className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
                onClick={() => {
                  setDraft(displayedContent);
                  setIsEditing(false);
                }}
                type="button"
              >
                Cancel
              </button>
            </div>
          </div>
        ) : null}
      </SlicePanel>
      {!isEditing ? (
        <SourceCodeViewer code={displayedContent} path={selectedPath} />
      ) : null}
    </div>
  );
}

function SourceSkeleton() {
  return (
    <SlicePanel>
      <div className="grid gap-3">
        <div className="h-5 w-2/5 animate-pulse rounded bg-slate-200" />
        <div className="h-12 animate-pulse rounded bg-slate-100" />
        <div className="h-12 animate-pulse rounded bg-slate-100" />
        <div className="h-12 animate-pulse rounded bg-slate-100" />
      </div>
    </SlicePanel>
  );
}

function NavigatorSkeleton() {
  return (
    <div className="grid gap-2">
      <div className="h-9 animate-pulse rounded-md bg-slate-100" />
      <div className="h-9 animate-pulse rounded-md bg-slate-100" />
      <div className="h-9 animate-pulse rounded-md bg-slate-100" />
    </div>
  );
}

function pathSearchValue(value: unknown) {
  return typeof value === "string" && value.trim()
    ? normalizeRepositoryPath(value)
    : "";
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
