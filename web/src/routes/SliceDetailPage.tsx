import { useState } from "react";
import { useAuth } from "@clerk/tanstack-react-start";
import {
  useNavigate,
  useParams,
  useSearch
} from "@tanstack/react-router";

import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { MobileDetailsProvider } from "../components/MobileDetails";
import { PageHeader } from "../components/PageHeader";
import { SliceAccessNotice } from "../components/slices/SliceAccessNotice";
import { SliceDescription } from "../components/slices/SliceDescriptionPanel";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePageHeader
} from "../components/slices/SlicePageParts";
import {
  useDraftChangesetController,
  type PendingEdit
} from "../components/source/SliceEditing";
import { cn } from "../lib/cn";
import { shortChangesetId } from "../lib/objectId";
import { useSlicePermissions } from "../lib/permissions";
import { sliceBreadcrumbItems } from "../lib/sliceRoutes";
import { useSelection } from "../state/selection";
import { CopyMoveProvider } from "./slice-detail/CopyMove";
import { canModifyPath } from "./slice-detail/DirectoryHeader";
import { FileFinder } from "./slice-detail/FileFinder";
import { HistoryDrawer } from "./slice-detail/HistoryDrawer";
import {
  ChangesetErrorToast,
  CreatingChangesetOverlay,
  MobileFileBar,
  SliceHeaderActions
} from "./slice-detail/SliceDetailChrome";
import { SliceFolderNavigator } from "./slice-detail/SliceFolderNavigator";
import { SliceSourceWorkspace } from "./slice-detail/SliceSourceWorkspace";
import { buildGitCloneHint } from "./slice-detail/sourceTree";
import { TriggersDrawer } from "./slice-detail/TriggersDrawer";
import { useGitCloneOrigin } from "./slice-detail/useGitCloneOrigin";
import { useSlicePathExplorer } from "./slice-detail/useSlicePathExplorer";

export function SliceDetailPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { isLoaded, isSignedIn } = useAuth();
  const { account, accounts } = useSelection();
  const params = useParams({ from: "/publicApp/slices/$account/$slice" });
  const search = useSearch({ from: "/publicApp/slices/$account/$slice" });
  const routeAccount = params.account ?? "";
  const routeSlice = params.slice ?? "";
  const selectedPath = search.path ?? "";
  const historyOpen = Boolean(search.history);
  const triggersOpen = Boolean(search.triggers);
  const blameOn = Boolean(search.blame);
  const blameAt = search.blameAt ?? "";

  const [isCreatingChangeset, setIsCreatingChangeset] = useState(false);
  const [changesetError, setChangesetError] = useState("");
  const gitCloneOrigin = useGitCloneOrigin();

  const explorer = useSlicePathExplorer({
    api,
    isLoaded,
    routeAccount,
    routeSlice,
    selectedPath,
    historyOpen,
    triggersOpen
  });

  const {
    sliceQuery,
    latestQuery,
    pathQuery,
    directoryQuery,
    fileQuery,
    slice,
    sliceRef,
    routeSliceRef,
    sliceId,
    sliceRouteParams,
    sliceRouteKey,
    sliceLabel,
    commitId,
    includedPaths,
    createDirectory,
    entry,
    isDirectory,
    isFile,
    currentEntries,
    fileContent,
    fileData,
    folderReadme,
    readmeLoading,
    showTree,
    setTreeChoice,
    phoneTreeBar,
    treePanelVisibility,
    workspaceVisibility,
    detailsOpen,
    setDetailsOpen,
    finderOpen,
    setFinderOpen,
    selectPath,
    setBlame,
    openHistory,
    closeHistory,
    openTriggers,
    closeTriggers,
    openLandedPath
  } = explorer;

  const slicePermissions = useSlicePermissions(slice ?? routeSliceRef);
  const canEdit = Boolean(account && slicePermissions.canPropose);
  const canConfigure = slicePermissions.canAdmin;

  const draftChangeset = useDraftChangesetController({
    api,
    commitId,
    sliceLabel,
    sliceRef,
    authorUsername: canEdit ? account : ""
  });

  async function stagePendingEdit(edit: PendingEdit) {
    if (!canEdit || !sliceRouteParams) {
      return;
    }
    setChangesetError("");
    setIsCreatingChangeset(true);
    try {
      const id = await draftChangeset.stageEditAndPersist(edit);
      if (id) {
        await navigate({
          params: { id: shortChangesetId(id) },
          to: "/cs/$id"
        });
      }
    } catch (error) {
      setChangesetError(
        error instanceof Error ? error.message : String(error)
      );
    } finally {
      setIsCreatingChangeset(false);
    }
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
          <SliceAccessNotice
            error={sliceQuery.error}
            isSignedIn={Boolean(isLoaded && isSignedIn)}
            sliceKey={`${routeAccount}/${routeSlice}`}
          />
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

  const gitCloneHint = buildGitCloneHint(
    slice.ref?.account,
    slice.ref?.slice,
    gitCloneOrigin
  );
  const sliceCrumbs = sliceBreadcrumbItems(sliceRef, accounts);

  const workspace = (
    <SliceSourceWorkspace
      blame={{ at: blameAt, on: blameOn, onChange: setBlame }}
      commitError={latestQuery.error}
      commitId={commitId}
      createDirectory={createDirectory}
      directoryEntries={currentEntries}
      directoryError={directoryQuery.error}
      entry={entry}
      fileContent={fileContent}
      fileData={fileData}
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
      pendingEdits={draftChangeset.edits}
      readme={folderReadme}
      readmeLoading={readmeLoading}
      rootOnPhone={!selectedPath}
      selectedPath={selectedPath}
      sliceRef={sliceRef}
    />
  );

  return (
    <MobileDetailsProvider
      onToggle={() => setDetailsOpen((open) => !open)}
      open={detailsOpen}
    >
      <section className="mx-auto w-full max-w-[100rem] lg:flex lg:h-[calc(100dvh-8rem)] lg:flex-col lg:overflow-hidden">
        {selectedPath ? (
          <MobileFileBar
            name={selectedPath.split("/").filter(Boolean).pop() ?? selectedPath}
            onBack={() => selectPath("")}
          />
        ) : null}
        {slice.archived ? (
          <div className="mb-3">
            <SliceNotice title="This slice is archived">
              It is read-only and hidden from listings. Its history is still here. An account owner can restore it with{" "}
              <code>gs slice unarchive {sliceLabel}</code>.
            </SliceNotice>
          </div>
        ) : null}
        <PageHeader
          className={cn(
            selectedPath && "max-lg:static",
            selectedPath && !detailsOpen && "max-lg:hidden"
          )}
          breadcrumb={
            <Breadcrumb
              items={[
                { label: "Home", to: "/" },
                ...(sliceCrumbs.length > 0 ? sliceCrumbs : [{ label: sliceLabel }])
              ]}
            />
          }
          primaryAction={
            <SliceHeaderActions
              canConfigure={canConfigure}
              commitId={commitId}
              gitUrl={gitCloneHint.url}
              isSignedIn={isSignedIn}
              onOpenFinder={() => setFinderOpen(true)}
              onOpenTriggers={openTriggers}
              sliceLabel={sliceLabel}
              sliceRouteParams={sliceRouteParams}
            />
          }
        />

        {slice.description ? (
          <SliceDescription
            className={cn(
              "mt-2 max-w-3xl whitespace-pre-line text-sm leading-6 text-slate-600 dark:text-zinc-400",
              selectedPath && "max-lg:hidden"
            )}
            text={slice.description}
          />
        ) : null}

        <>
          <div
            className={cn(
              "mt-4 grid gap-4 lg:min-h-0 lg:flex-1",
              showTree
                ? "lg:grid-cols-[19rem_minmax(0,1fr)]"
                : "lg:grid-cols-[2.75rem_minmax(0,1fr)]"
            )}
          >
            <button
              aria-controls="slice-file-tree-panel"
              aria-expanded={false}
              aria-label="Show files"
              className={cn(
                "hidden h-full min-h-0 flex-col items-center gap-2 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-1.5 py-3 text-xs font-semibold text-slate-600 dark:text-zinc-400 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]",
                showTree ? "" : "lg:flex"
              )}
              onClick={() => setTreeChoice(true)}
              title="Show files"
              type="button"
            >
              <span aria-hidden="true" className="text-sm leading-none">
                »
              </span>
              <span className="[writing-mode:vertical-rl]">Files</span>
            </button>
            {phoneTreeBar ? (
              <button
                aria-controls="slice-file-tree-panel"
                aria-expanded={showTree}
                className="order-1 flex items-center justify-between rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-4 py-3 text-left text-sm font-semibold text-zinc-950 dark:text-zinc-50 lg:hidden"
                onClick={() => setTreeChoice(!showTree)}
                type="button"
              >
                <span>Files</span>
                <span aria-hidden="true" className="text-xs font-normal text-slate-500 dark:text-zinc-400">
                  {showTree ? "Hide" : "Show"}
                </span>
              </button>
            ) : null}
            <aside
              className={cn(
                "min-w-0 lg:h-full lg:min-h-0 lg:overflow-y-auto",
                phoneTreeBar && "max-lg:order-2",
                treePanelVisibility
              )}
              id="slice-file-tree-panel"
            >
              <SliceFolderNavigator
                api={api}
                commitId={commitId}
                includedPaths={includedPaths}
                isLatestLoading={latestQuery.isPending}
                isSelectedDirectory={isDirectory}
                onCollapse={() => setTreeChoice(false)}
                onSelectPath={selectPath}
                selectedPath={selectedPath}
                sliceId={sliceId || sliceRouteKey}
                sliceRef={sliceRef}
              />
            </aside>

            <div
              className={cn(
                "min-w-0 lg:h-full lg:min-h-0 lg:overflow-y-auto",
                workspaceVisibility
              )}
            >
              <CopyMoveProvider
                canModify={(path) => canModifyPath(includedPaths, path)}
                canWriteAccount={slicePermissions.canSubmitToAccount}
                commitId={commitId}
                enabled={Boolean(account && slicePermissions.canSubmit)}
                onLanded={openLandedPath}
                slice={sliceRef}
              >
                {workspace}
              </CopyMoveProvider>
            </div>
          </div>
          {finderOpen && commitId && sliceRef ? (
            <FileFinder
              api={api}
              commitId={commitId}
              includedPaths={includedPaths}
              onClose={() => setFinderOpen(false)}
              onSelectPath={selectPath}
              slice={sliceRef}
            />
          ) : null}
          <TriggersDrawer
            api={api}
            folder={triggersFolder(selectedPath, isFile, includedPaths)}
            onClose={closeTriggers}
            open={triggersOpen}
          />
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
        {isCreatingChangeset ? <CreatingChangesetOverlay /> : null}
        {changesetError ? (
          <ChangesetErrorToast
            message={changesetError}
            onDismiss={() => setChangesetError("")}
          />
        ) : null}
      </section>
    </MobileDetailsProvider>
  );
}

export function triggersFolder(selectedPath: string, isFile: boolean, includedPaths: string[]) {
  let folder = selectedPath || includedPaths[0] || "";
  if (selectedPath && isFile) {
    folder = selectedPath.slice(0, selectedPath.lastIndexOf("/")) || folder;
  }
  folder = "/" + folder.replace(/^\/+|\/+$/g, "");
  return folder === "/" ? "" : folder;
}
