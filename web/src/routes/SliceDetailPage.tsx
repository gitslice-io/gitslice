import { useEffect, useState, useMemo } from "react";
import { useAuth } from "@clerk/tanstack-react-start";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import type { SliceRef, TreeEntry } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePageHeader,
  SlicePanel,
  getErrorMessage,
  sliceDisplayName
} from "../components/slices/SlicePageParts";
import {
  decodeBase64File,
  entryKindLabel,
  isSliceProjectionDirectoryPath,
  listDirectoryAll,
  normalizeRepositoryPath,
  syntheticDirectoryEntry
} from "../components/source/sourceUtils";
import {
  useDraftChangesetController,
  type PendingEdit
} from "../components/source/SliceEditing";
import { GLOBAL_REF_NAME } from "../lib/globalRef";
import { shortChangesetId, shortHash } from "../lib/objectId";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { useSelection } from "../state/selection";
import { cn } from "../lib/cn";
import { pathSearchValue, buildGitCloneHint } from "./slice-detail/sourceTree";
import { CheckoutMenu } from "./slice-detail/CheckoutMenu";
import { PendingChangesBanner } from "./slice-detail/PendingChangesBanner";
import { HistoryDrawer } from "./slice-detail/HistoryDrawer";
import { SliceFolderNavigator } from "./slice-detail/SliceFolderNavigator";
import { SliceSourceWorkspace } from "./slice-detail/SliceSourceWorkspace";

interface SliceParams {
  account?: string;
  slice?: string;
}

interface SliceSearch {
  path?: unknown;
}

export function SliceDetailPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { isLoaded, isSignedIn } = useAuth();
  const { account } = useSelection();
  const params = useParams({ strict: false }) as SliceParams;
  const search = useSearch({ strict: false }) as SliceSearch;
  const routeAccount = params.account ?? "";
  const routeSlice = params.slice ?? "";
  const routeSliceRef = useMemo(
    () =>
      routeAccount && routeSlice
        ? { account: routeAccount, slice: routeSlice }
        : undefined,
    [routeAccount, routeSlice]
  );
  const selectedPath = pathSearchValue(search.path);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [showTree, setShowTree] = useState(true);
  const [mobileFilesOpen, setMobileFilesOpen] = useState(false);
  const canEdit = Boolean(isLoaded && isSignedIn && account);

  useEffect(() => {
    setHistoryOpen(false);
  }, [selectedPath]);

  const sliceQuery = useQuery({
    enabled: Boolean(isLoaded && routeSliceRef),
    queryKey: ["sliceRef", routeAccount, routeSlice],
    queryFn: () => api.resolveSlice({ ref: routeSliceRef })
  });

  const latestQuery = useQuery({
    enabled: Boolean(isLoaded && sliceQuery.isSuccess),
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
  const sliceRef = slice?.ref ?? routeSliceRef;
  const sliceId = slice?.id ?? "";
  const sliceRouteParams = toSliceRouteParams(sliceRef);
  const sliceRouteKey = sliceRouteParams
    ? `${sliceRouteParams.account}:${sliceRouteParams.slice}`
    : `${routeAccount}:${routeSlice}`;
  const sliceLabel = sliceDisplayName(slice);
  const commitId = latestQuery.data?.commitId ?? "";
  const draftChangeset = useDraftChangesetController({
    api,
    commitId,
    sliceLabel,
    sliceRef,
    authorUsername: canEdit ? account : ""
  });
  const pendingEdits = draftChangeset.edits;
  const isProjectedDirectoryPath = isSliceProjectionDirectoryPath(
    selectedPath,
    slice?.definition?.includedPaths ?? []
  );

  const pathQuery = useQuery({
    enabled: Boolean(
      commitId &&
        selectedPath &&
        !isProjectedDirectoryPath &&
        sliceRef?.account &&
        sliceRef?.slice
    ),
    queryKey: [
      "slicePath",
      sliceRouteKey,
      commitId,
      selectedPath,
      sliceRef?.account,
      sliceRef?.slice
    ],
    queryFn: () => api.resolvePath({ commitId, path: selectedPath, slice: sliceRef })
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
      sliceRouteKey,
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
    enabled: Boolean(
      commitId && selectedPath && isFile && sliceRef?.account && sliceRef?.slice
    ),
    queryKey: [
      "sliceFile",
      sliceRouteKey,
      commitId,
      selectedPath,
      sliceRef?.account,
      sliceRef?.slice
    ],
    queryFn: () => api.readFile({ commitId, path: selectedPath, slice: sliceRef })
  });

  function selectPath(path: string) {
    if (!sliceRouteParams) {
      return;
    }

    void navigate({
      params: sliceRouteParams as never,
      search: path ? ({ path } as never) : ({} as never),
      to: "/slices/$account/$slice"
    });
  }

  function stagePendingEdit(edit: PendingEdit) {
    if (!canEdit) {
      return;
    }
    draftChangeset.stageEdit(edit);
  }

  if (!isLoaded || sliceQuery.isPending) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
        <SliceLoadingBlock />
      </section>
    );
  }

  if (sliceQuery.isError) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
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
      <section className="mx-auto w-full max-w-[100rem]">
        <SlicePageHeader title="Slice Home" />
        <div className="mt-8">
          <SliceNotice title="Slice not found">
            No slice was returned for {sliceRouteKey || "unknown"}.
          </SliceNotice>
        </div>
      </section>
    );
  }

  const includedPaths = slice.definition?.includedPaths ?? [];
  const gitCloneHint = buildGitCloneHint(slice.ref?.account, slice.ref?.slice);
  const currentEntries = directoryQuery.data ?? [];
  const projectedRoots = includedPaths
    .map((includedPath) => includedPath.replace(/^\/+|\/+$/g, ""))
    .filter(Boolean);
  const createDirectory =
    selectedPath || (projectedRoots.length === 1 ? projectedRoots[0] : "");

  return (
    <section className="mx-auto w-full max-w-[100rem] lg:flex lg:h-[calc(100dvh-8rem)] lg:flex-col lg:overflow-hidden">
      <PageHeader
        breadcrumb={
          <Breadcrumb
            items={[
              { label: "Slices", to: "/slices" },
              { label: sliceLabel }
            ]}
          />
        }
        primaryAction={
          <>
            <button
              aria-controls="slice-file-tree-panel"
              aria-expanded={mobileFilesOpen}
              className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98] lg:hidden"
              onClick={() => setMobileFilesOpen(true)}
              type="button"
            >
              Files
            </button>
            <Link
              className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
              search={{ slice: sliceLabel } as never}
              to="/changesets"
            >
              Changesets
            </Link>
            {isSignedIn && sliceRouteParams ? (
              <Link
                className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
                params={sliceRouteParams as never}
                to="/slices/$account/$slice/agents"
              >
                Agents
              </Link>
            ) : null}
            {canEdit && sliceRouteParams ? (
              <Link
                className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
                params={sliceRouteParams as never}
                to="/slices/$account/$slice/settings"
              >
                Settings
              </Link>
            ) : null}
            <CheckoutMenu gitUrl={gitCloneHint.url} sliceRef={sliceLabel} />
          </>
        }
      />

      {pendingEdits.length ? (
        <PendingChangesBanner
          changesetRef={
            draftChangeset.changesetLabel ||
            shortChangesetId(draftChangeset.changesetId)
          }
          count={pendingEdits.length}
          saveStatus={draftChangeset.saveStatus}
        />
      ) : null}

      <>
          <div
            className={[
              "mt-4 grid gap-4 lg:min-h-0 lg:flex-1",
              showTree
                ? "lg:grid-cols-[19rem_minmax(0,1fr)]"
                : "lg:grid-cols-[2.75rem_minmax(0,1fr)]"
            ].join(" ")}
          >
            {mobileFilesOpen ? (
              <button
                aria-label="Close files"
                className="fixed inset-0 z-30 bg-black/30 lg:hidden"
                onClick={() => setMobileFilesOpen(false)}
                type="button"
              />
            ) : null}
            <button
              aria-controls="slice-file-tree-panel"
              aria-expanded={false}
              aria-label="Show files"
              className={[
                "hidden h-full min-h-0 flex-col items-center gap-2 rounded-md border border-slate-200 bg-white px-1.5 py-3 text-xs font-semibold text-slate-600 transition hover:bg-slate-50 active:scale-[0.98]",
                showTree ? "" : "lg:flex"
              ].join(" ")}
              onClick={() => setShowTree(true)}
              title="Show files"
              type="button"
            >
              <span aria-hidden="true" className="text-sm leading-none">
                »
              </span>
              <span className="[writing-mode:vertical-rl]">Files</span>
            </button>
            <aside
              className={[
                "fixed inset-y-0 left-0 z-40 w-80 max-w-[85%] transform overflow-y-auto bg-slate-50 p-4 shadow-xl transition-transform duration-200",
                mobileFilesOpen ? "translate-x-0" : "-translate-x-full",
                "lg:static lg:z-auto lg:w-auto lg:max-w-none lg:translate-x-0 lg:bg-transparent lg:p-0 lg:shadow-none lg:transition-none lg:h-full lg:min-h-0 lg:overflow-y-auto",
                showTree ? "" : "lg:hidden"
              ].join(" ")}
              id="slice-file-tree-panel"
            >
              <div className="mb-3 flex items-center justify-between lg:hidden">
                <span className="text-sm font-semibold text-zinc-950">
                  Files
                </span>
                <button
                  aria-label="Close files"
                  className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
                  onClick={() => setMobileFilesOpen(false)}
                  type="button"
                >
                  Close
                </button>
              </div>
              <SliceFolderNavigator
                api={api}
                commitId={commitId}
                includedPaths={includedPaths}
                isLatestLoading={latestQuery.isPending}
                isSelectedDirectory={isDirectory}
                onCollapse={() => setShowTree(false)}
                onSelectPath={(nextPath) => {
                  selectPath(nextPath);
                  setMobileFilesOpen(false);
                }}
                selectedPath={selectedPath}
                sliceId={sliceId || sliceRouteKey}
                sliceRef={sliceRef}
              />
            </aside>

            <div className="min-w-0 lg:h-full lg:min-h-0 lg:overflow-y-auto">
              <SliceSourceWorkspace
                commitError={latestQuery.error}
                commitId={commitId}
                createDirectory={createDirectory}
                directoryEntries={currentEntries}
                directoryError={directoryQuery.error}
                entry={entry}
                fileContent={decodeBase64File(fileQuery.data?.data)}
                fileError={fileQuery.error}
                includedPaths={includedPaths}
                isDirectoryLoading={directoryQuery.isPending}
                isFileLoading={fileQuery.isPending}
                isLatestLoading={latestQuery.isPending}
                isPathLoading={pathQuery.isLoading}
                onOpenHistory={() => setHistoryOpen(true)}
                onSelectPath={selectPath}
                onStageEdit={canEdit ? stagePendingEdit : undefined}
                pathError={pathQuery.error}
                pendingEdits={pendingEdits}
                selectedPath={selectedPath}
              />
            </div>
          </div>
          <HistoryDrawer
            api={api}
            commitId={commitId}
            onClose={() => setHistoryOpen(false)}
            open={historyOpen}
            selectedPath={selectedPath}
            sliceId={sliceId || sliceRouteKey}
            sliceLabel={sliceLabel}
            sliceRef={sliceRef}
          />
      </>
    </section>
  );
}
