import { useEffect, useState, useMemo } from "react";
import { useAuth } from "@clerk/tanstack-react-start";
import {
  Link,
  useNavigate,
  useParams,
  useRouter,
  useSearch,
} from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import { AnalyticsEvents, AnalyticsProps } from "../analytics/events";
import { capture } from "../analytics/posthog";
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
  sliceDisplayName,
} from "../components/slices/SlicePageParts";
import {
  decodeBase64File,
  entryKindLabel,
  isSliceProjectionDirectoryPath,
  listDirectoryAll,
  normalizeRepositoryPath,
  syntheticDirectoryEntry,
} from "../components/source/sourceUtils";
import {
  useDraftChangesetController,
  type PendingEdit,
} from "../components/source/SliceEditing";
import { GLOBAL_REF_NAME } from "../lib/globalRef";
import { shortHash } from "../lib/objectId";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { useSelection } from "../state/selection";
import { cn } from "../lib/cn";
import { pathSearchValue, buildGitCloneHint } from "./slice-detail/sourceTree";
import { useGitCloneOrigin } from "./slice-detail/useGitCloneOrigin";
import { CheckoutMenu } from "./slice-detail/CheckoutMenu";
import { HistoryDrawer } from "./slice-detail/HistoryDrawer";
import { SliceFolderNavigator } from "./slice-detail/SliceFolderNavigator";
import { SliceSourceWorkspace } from "./slice-detail/SliceSourceWorkspace";

interface SliceParams {
  account?: string;
  slice?: string;
}

interface SliceSearch {
  path?: unknown;
  history?: unknown;
}

export function SliceDetailPage() {
  const api = useApi();
  const navigate = useNavigate();
  const router = useRouter();
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
    [routeAccount, routeSlice],
  );
  const selectedPath = pathSearchValue(search.path);
  // The history view's open state lives in the URL (the `history` search param)
  // rather than local state so the browser/mobile back gesture closes it instead
  // of navigating away from the slice page entirely.
  const historyOpen = Boolean(search.history);
  const [showTree, setShowTree] = useState(true);
  const gitCloneOrigin = useGitCloneOrigin();
  const canEdit = Boolean(isLoaded && isSignedIn && account);

  const sliceQuery = useQuery({
    enabled: Boolean(isLoaded && routeSliceRef),
    queryKey: ["sliceRef", routeAccount, routeSlice],
    queryFn: () => api.resolveSlice({ ref: routeSliceRef }),
  });

  const latestQuery = useQuery({
    // getRef(refs/global/main) is independent of the slice, so it does not need
    // to wait for resolveSlice to succeed. Firing it in parallel removes one hop
    // from the file-view request waterfall (resolveSlice/getRef both feed the
    // commit + slice that resolvePath/readFile need). It stays gated on the
    // slice not having errored so we skip the fetch for missing/forbidden slices.
    enabled: Boolean(isLoaded && routeSliceRef && !sliceQuery.isError),
    queryKey: ["globalRef", GLOBAL_REF_NAME],
    queryFn: async () => {
      const ref = await api.getRef({ refName: GLOBAL_REF_NAME });
      if (!ref.commitId) {
        throw new Error("Latest global state did not return a commit id.");
      }
      return ref;
    },
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
    authorUsername: canEdit ? account : "",
  });
  const pendingEdits = draftChangeset.edits;
  const isProjectedDirectoryPath = isSliceProjectionDirectoryPath(
    selectedPath,
    slice?.definition?.includedPaths ?? [],
  );

  useEffect(() => {
    if (!sliceId) {
      return;
    }

    capture(AnalyticsEvents.sliceViewed, {
      [AnalyticsProps.sliceId]: sliceId,
    });
  }, [sliceId]);

  const pathQuery = useQuery({
    enabled: Boolean(
      commitId &&
      selectedPath &&
      !isProjectedDirectoryPath &&
      sliceRef?.account &&
      sliceRef?.slice,
    ),
    queryKey: [
      "slicePath",
      sliceRouteKey,
      commitId,
      selectedPath,
      sliceRef?.account,
      sliceRef?.slice,
    ],
    queryFn: () =>
      api.resolvePath({ commitId, path: selectedPath, slice: sliceRef }),
  });

  const entry = isProjectedDirectoryPath
    ? syntheticDirectoryEntry(selectedPath)
    : pathQuery.data?.entry;
  const isDirectory = entry?.kind === "ENTRY_KIND_DIRECTORY";
  const isFile = entry?.kind === "ENTRY_KIND_FILE";

  const directoryQuery = useQuery({
    enabled: Boolean(
      commitId && sliceRef?.account && sliceRef?.slice && isDirectory,
    ),
    queryKey: [
      "sliceDirectory",
      sliceRouteKey,
      commitId,
      selectedPath,
      sliceRef?.account,
      sliceRef?.slice,
    ],
    queryFn: () =>
      listDirectoryAll(api, {
        allowMissingDirectory: isProjectedDirectoryPath,
        commitId,
        path: selectedPath,
        slice: sliceRef,
      }),
  });

  const fileQuery = useQuery({
    // Fire readFile in parallel with resolvePath rather than waiting for it to
    // report the entry is a file. Gating on isFile serialized the two slowest
    // RPCs into a waterfall (resolvePath ~800ms -> readFile ~600ms). We only
    // suppress it once resolvePath has proven the path is a directory
    // (isDirectory); while the kind is still unknown it runs optimistically.
    // readFile on a directory returns an inert error we never render, since the
    // file view is shown only when isFile.
    enabled: Boolean(
      commitId &&
      selectedPath &&
      !isDirectory &&
      !isProjectedDirectoryPath &&
      sliceRef?.account &&
      sliceRef?.slice,
    ),
    queryKey: [
      "sliceFile",
      sliceRouteKey,
      commitId,
      selectedPath,
      sliceRef?.account,
      sliceRef?.slice,
    ],
    queryFn: () =>
      api.readFile({ commitId, path: selectedPath, slice: sliceRef }),
  });

  function selectPath(path: string) {
    if (!sliceRouteParams) {
      return;
    }

    void navigate({
      params: sliceRouteParams as never,
      search: path ? ({ path } as never) : ({} as never),
      to: "/slices/$account/$slice",
    });
  }

  function openHistory() {
    if (!sliceRouteParams) {
      return;
    }
    // Push a new history entry so "back" (including the mobile swipe) pops the
    // history view and returns to this slice page instead of leaving it.
    void navigate({
      params: sliceRouteParams as never,
      search: {
        ...(selectedPath ? { path: selectedPath } : {}),
        history: "1",
      } as never,
      to: "/slices/$account/$slice",
    });
  }

  function closeHistory() {
    if (historyOpen) {
      // Mirror the back gesture: pop the entry openHistory pushed.
      router.history.back();
    }
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
  const gitCloneHint = buildGitCloneHint(
    slice.ref?.account,
    slice.ref?.slice,
    gitCloneOrigin
  );
  const currentEntries = directoryQuery.data ?? [];
  const projectedRoots = includedPaths
    .map((includedPath) => includedPath.replace(/^\/+|\/+$/g, ""))
    .filter(Boolean);
  const createDirectory =
    selectedPath || (projectedRoots.length === 1 ? projectedRoots[0] : "");
  const treePanelVisibility = selectedPath
    ? showTree
      ? "hidden lg:block"
      : "hidden"
    : showTree
      ? "block"
      : "block lg:hidden";
  const workspaceVisibility = selectedPath ? "block" : "hidden lg:block";

  return (
    <section className="mx-auto w-full max-w-[100rem] lg:flex lg:h-[calc(100dvh-8rem)] lg:flex-col lg:overflow-hidden">
      <PageHeader
        breadcrumb={
          <Breadcrumb
            items={[{ label: "Home", to: "/" }, { label: sliceLabel }]}
          />
        }
        primaryAction={
          <>
            <Link
              className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
              search={{ slice: sliceLabel } as never}
              to="/changesets"
            >
              Changesets
            </Link>
            {isSignedIn && sliceRouteParams ? (
              <Link
                className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
                params={sliceRouteParams as never}
                to="/slices/$account/$slice/agents"
              >
                Conversations
              </Link>
            ) : null}
            {canEdit && sliceRouteParams ? (
              <Link
                className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
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

      <>
        <div
          className={cn(
            "mt-4 grid gap-4 lg:min-h-0 lg:flex-1",
            showTree
              ? "lg:grid-cols-[19rem_minmax(0,1fr)]"
              : "lg:grid-cols-[2.75rem_minmax(0,1fr)]",
          )}
        >
          <button
            aria-controls="slice-file-tree-panel"
            aria-expanded={false}
            aria-label="Show files"
            className={cn(
              "hidden h-full min-h-0 flex-col items-center gap-2 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-1.5 py-3 text-xs font-semibold text-slate-600 dark:text-zinc-400 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]",
              showTree ? "" : "lg:flex",
            )}
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
            className={cn(
              "min-w-0 lg:h-full lg:min-h-0 lg:overflow-y-auto",
              treePanelVisibility,
            )}
            id="slice-file-tree-panel"
          >
            <SliceFolderNavigator
              api={api}
              commitId={commitId}
              includedPaths={includedPaths}
              isLatestLoading={latestQuery.isPending}
              isSelectedDirectory={isDirectory}
              onCollapse={() => setShowTree(false)}
              onSelectPath={selectPath}
              selectedPath={selectedPath}
              sliceId={sliceId || sliceRouteKey}
              sliceRef={sliceRef}
            />
          </aside>

          <div
            className={cn(
              "min-w-0 lg:h-full lg:min-h-0 lg:overflow-y-auto",
              workspaceVisibility,
            )}
          >
            <div className="mb-3 lg:hidden">
              <button
                className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
                onClick={() => selectPath("")}
                type="button"
              >
                ← Files
              </button>
            </div>
            <SliceSourceWorkspace
              commitError={latestQuery.error}
              commitId={commitId}
              createDirectory={createDirectory}
              directoryEntries={currentEntries}
              directoryError={directoryQuery.error}
              entry={entry}
              fileContent={decodeBase64File(fileQuery.data?.data)}
              fileData={fileQuery.data?.data ?? ""}
              fileError={fileQuery.error}
              includedPaths={includedPaths}
              isDirectoryLoading={directoryQuery.isPending}
              isFileLoading={fileQuery.isPending}
              isLatestLoading={latestQuery.isPending}
              isPathLoading={pathQuery.isLoading}
              onOpenHistory={openHistory}
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
          onClose={closeHistory}
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
