import { useQuery } from "@tanstack/react-query";
import {
  useNavigate,
  useParams,
  useRouter,
  useSearch
} from "@tanstack/react-router";
import { useAuth } from "@clerk/tanstack-react-start";
import {
  useCallback,
  useEffect,
  useMemo,
  useState
} from "react";

import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { type ViewMode } from "../components/diff/diffViewShared";
import { DiffViewer } from "../components/diff/DiffViewer";
import { type DiffFile } from "../components/diff/parseDiff";
import { MobileDetailsProvider } from "../components/MobileDetails";
import { PageHeader } from "../components/PageHeader";
import { cn } from "../lib/cn";
import { conversationsEnabled } from "../lib/features";
import { shortChangesetId } from "../lib/objectId";
import { useChangesetPermissions } from "../lib/permissions";
import { changesetPageTitle } from "../lib/socialMeta";
import { useMediaQuery } from "../lib/useMediaQuery";
import {
  changesetQuery as changesetQuery_,
  dependentChangesetsQuery,
  type DependentChangeset
} from "../queries/changesets";
import { useSelection } from "../state/selection";
import {
  ChangesetSkeleton,
  ChecksPanel,
  ConversationDrawer,
  HeaderCard,
  PageMessage,
  PatchsetComparePanel,
  TriggerRunsPanel
} from "./changeset-detail";
import { ChangesetLoadError } from "./changeset-detail/ChangesetLoadError";
import { samePath } from "./changeset-detail/diffFiles";
import {
  FileVersionDrawer,
  fileVersionSides,
  type FileVersionSide,
  type FileVersionTarget
} from "./changeset-detail/FileVersionDrawer";
import { sortedPatchsets } from "./changeset-detail/patchsetUtils";
import {
  changesetBreadcrumbItems,
  changesetSliceSearch,
  isTerminalStatus
} from "./changeset-detail/status";
import { useChangesetActions } from "./changeset-detail/useChangesetActions";
import { usePatchsetDiffComparison } from "./changeset-detail/usePatchsetDiffComparison";

const NO_DEPENDENTS: DependentChangeset[] = [];

export function ChangesetDetailPage() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const viewerSignedOut = isLoaded && !isSignedIn;
  const { accounts: viewerAccounts } = useSelection();
  const params = useParams({ from: "/publicApp/cs/$id" });
  const changesetId = params.id ?? "";
  const navigate = useNavigate();
  const router = useRouter();
  const search = useSearch({ from: "/publicApp/cs/$id" });
  const [detailsOpen, setDetailsOpen] = useState(false);
  const conversationOpen = conversationsEnabled && Boolean(search.conversation);
  const isWide = useMediaQuery("(min-width: 1280px)");

  const changesetQuery = useQuery({
    ...changesetQuery_(api, changesetId),
    enabled: Boolean(isLoaded && changesetId),
    refetchInterval: (query) =>
      query.state.data?.status === "pending_publish" ? 2500 : false
  });

  const changeset = changesetQuery.data;
  const authoringSlice = changeset?.authoringSlice;
  const changesetPermissions = useChangesetPermissions(changeset);

  const pageTitle = changeset
    ? changesetPageTitle(changeset.title ?? "", authoringSlice?.account ?? "", authoringSlice?.slice ?? "")
    : "";
  useEffect(() => {
    if (pageTitle) {
      document.title = pageTitle;
    }
  }, [pageTitle]);

  useEffect(() => {
    const id = changeset?.id ?? "";
    const requested = changesetId.toLowerCase().replace(/^cs_/, "");
    if (!id || !requested || id.replace(/^cs_/, "").startsWith(requested)) {
      return;
    }
    void navigate({
      params: { id: shortChangesetId(id) },
      replace: true,
      search,
      to: "/cs/$id"
    });
  }, [changeset?.id, changesetId, navigate, search]);

  const sliceChangesetsQuery = useQuery({
    enabled: Boolean(
      changeset?.id && authoringSlice?.account && authoringSlice?.slice
    ),
    ...dependentChangesetsQuery(api, authoringSlice, changeset?.id ?? "")
  });

  const dependentChangesets = sliceChangesetsQuery.data ?? NO_DEPENDENTS;
  const canonicalChangesetId = changeset?.id || changesetId;
  const sliceSearch = changeset ? changesetSliceSearch(changeset) : "";

  const {
    patchsets,
    fromPatchset,
    selectedToPatchset,
    selectedFromPatchset,
    selectedToPatchsetMetadata,
    changedPaths,
    fileMetadataByPath,
    usesLazyFileDiffs,
    comparisonKey,
    diffQuery,
    fileStates,
    requestFileDiff,
    retryFileDiff
  } = usePatchsetDiffComparison({
    api,
    changeset,
    canonicalChangesetId,
    fromSearch: search.from,
    toSearch: search.to,
    focusFilePath: search.file
  });

  const updateCompareSearch = useCallback(
    (next: { from?: string; to?: string }) => {
      void navigate({
        params: { id: changesetId },
        replace: true,
        search: (prev) => {
          const merged = { ...prev, ...next };
          for (const key of ["from", "to"] as const) {
            if (!merged[key]) {
              delete merged[key];
            }
          }
          return merged;
        },
        to: "/cs/$id"
      });
    },
    [changesetId, navigate]
  );
  const handleFromPatchsetChange = useCallback(
    (value: string) => updateCompareSearch({ from: value }),
    [updateCompareSearch]
  );
  const handleToPatchsetChange = useCallback(
    (value: string) => updateCompareSearch({ to: value }),
    [updateCompareSearch]
  );

  const openConversation = useCallback(() => {
    void navigate({
      params: { id: changesetId },
      search: (prev) => ({
        ...prev,
        conversation: true
      }),
      to: "/cs/$id"
    });
  }, [changesetId, navigate]);
  const closeConversation = useCallback(() => {
    if (conversationOpen) {
      router.history.back();
    }
  }, [conversationOpen, router.history]);
  const toggleConversation = useCallback(() => {
    if (conversationOpen) {
      closeConversation();
      return;
    }
    openConversation();
  }, [closeConversation, conversationOpen, openConversation]);

  const [viewedFile, setViewedFile] = useState<FileVersionTarget>();
  const fileViewPath = search.view ?? "";
  const fileViewTarget = useMemo<FileVersionTarget | undefined>(() => {
    if (!fileViewPath) {
      return undefined;
    }
    if (viewedFile && samePath(viewedFile.path, fileViewPath)) {
      return viewedFile;
    }
    const metadataPath = changedPaths.find((path) => samePath(path, fileViewPath));
    return { path: fileViewPath, ...(metadataPath ? fileMetadataByPath.get(metadataPath) : {}) };
  }, [changedPaths, fileMetadataByPath, fileViewPath, viewedFile]);
  const fileViewSide: FileVersionSide =
    search.side ??
    (fileVersionSides(fileViewTarget?.changeKind).original ? "original" : "changed");
  const openFileView = useCallback(
    (file: DiffFile) => {
      setViewedFile({ changeKind: file.changeKind, oldPath: file.oldPath, path: file.path });
      void navigate({
        params: { id: changesetId },
        search: (prev) => ({
          ...prev,
          side: fileVersionSides(file.changeKind).original ? "original" : "changed",
          view: file.path
        }),
        to: "/cs/$id"
      });
    },
    [changesetId, navigate]
  );
  const setFileViewSide = useCallback(
    (side: FileVersionSide) => {
      void navigate({
        params: { id: changesetId },
        replace: true,
        search: (prev) => ({ ...prev, side }),
        to: "/cs/$id"
      });
    },
    [changesetId, navigate]
  );
  const diffViewMode: ViewMode = search.diff === "split" ? "split" : "unified";
  const setDiffViewMode = useCallback(
    (mode: ViewMode) => {
      void navigate({
        params: { id: changesetId },
        replace: true,
        search: (prev) => ({
          ...prev,
          diff: mode === "split" ? ("split" as const) : undefined
        }),
        to: "/cs/$id"
      });
    },
    [changesetId, navigate]
  );
  const closeFileView = useCallback(() => {
    if (fileViewPath) {
      router.history.back();
    }
  }, [fileViewPath, router.history]);

  const {
    abandonReason,
    setAbandonReason,
    actionError,
    actionBusy,
    mergePending,
    abandonPending,
    merge,
    submitAbandon,
    saveInfo
  } = useChangesetActions({
    api,
    changeset,
    changesetId,
    canonicalChangesetId
  });

  if (!changesetId) {
    return (
      <PageMessage
        title="Missing changeset"
        message="No changeset id was provided."
      />
    );
  }

  if (!isLoaded || changesetQuery.isPending) {
    return <ChangesetSkeleton />;
  }

  if (changesetQuery.isError) {
    return (
      <ChangesetLoadError
        changesetId={changesetId}
        error={changesetQuery.error}
        signedIn={Boolean(isLoaded && isSignedIn)}
      />
    );
  }

  if (!changeset) {
    return (
      <PageMessage
        title="Changeset not found"
        message="The API returned no changeset."
      />
    );
  }

  const terminal = isTerminalStatus(changeset.status);
  return (
    <MobileDetailsProvider
      onToggle={() => setDetailsOpen((open) => !open)}
      open={detailsOpen}
    >
      <section className="mx-auto w-full max-w-[100rem]">
        <PageHeader
          className={cn(!detailsOpen && "max-lg:hidden")}
          breadcrumb={
            <Breadcrumb
              items={changesetBreadcrumbItems({
                changeset,
                sliceSearch,
                viewerAccounts
              })}
            />
          }
        />

        <HeaderCard
          abandonReason={abandonReason}
          actionBusy={actionBusy}
          actionError={actionError}
          abandonPending={abandonPending}
          canAbandon={changesetPermissions.canAbandon}
          canSubmit={changesetPermissions.canSubmit}
          changeset={changeset}
          dependentChangesets={dependentChangesets}
          mergePending={mergePending}
          onAbandon={submitAbandon}
          onAbandonReasonChange={setAbandonReason}
          onMerge={merge}
          onSaveInfo={changesetPermissions.canEditInfo ? saveInfo : undefined}
          patchsetCompare={
            <PatchsetComparePanel
              conversationOpen={conversationOpen}
              currentPatchsetId={changeset.currentPatchsetId}
              fromPatchset={fromPatchset}
              onFromPatchsetChange={handleFromPatchsetChange}
              onToPatchsetChange={handleToPatchsetChange}
              onToggleConversation={conversationsEnabled ? toggleConversation : undefined}
              patchsets={patchsets}
              toPatchset={selectedToPatchset}
            />
          }
          terminal={terminal}
        />

        <div className={cn(!detailsOpen && "max-lg:hidden")}>
          <ChecksPanel
            changesetId={canonicalChangesetId}
            patchsetId={selectedToPatchset}
            signedOut={viewerSignedOut}
          />
          <TriggerRunsPanel
            changesetId={canonicalChangesetId}
            patchsetId={selectedToPatchset}
          />
        </div>

        <div className={cn(isWide && conversationOpen && "flex items-start gap-3")}>
          <div className={cn(isWide && conversationOpen && "min-w-0 flex-1")}>
            <DiffViewer
              diffResponse={diffQuery.data}
              error={diffQuery.error}
              fileStates={fileStates}
              focusFilePath={search.file}
              isError={diffQuery.isError}
              isLoading={!usesLazyFileDiffs && diffQuery.isPending}
              key={comparisonKey}
              onFileNeeded={requestFileDiff}
              onFileRetry={retryFileDiff}
              onViewFile={openFileView}
              onViewModeChange={setDiffViewMode}
              viewedScope={canonicalChangesetId}
              viewMode={diffViewMode}
            />
          </div>
          <ConversationDrawer
            docked={isWide}
            enabled={conversationsEnabled && Boolean(isLoaded)}
            fromPatchsetId={fromPatchset}
            onClose={closeConversation}
            open={conversationOpen}
            patchsets={patchsets}
            selectedPatchsetId={selectedToPatchset}
          />
        </div>
        <FileVersionDrawer
          api={api}
          changedLabel={selectedFromPatchset ? `Patchset ${selectedToPatchsetMetadata?.number ?? ""}` : "Changed"}
          changedTreeId={selectedToPatchsetMetadata?.resultTreeId}
          onClose={closeFileView}
          onSideChange={setFileViewSide}
          originalLabel={selectedFromPatchset ? `Patchset ${selectedFromPatchset.number ?? ""}` : "Original"}
          originalTreeId={
            selectedFromPatchset ? selectedFromPatchset.resultTreeId : selectedToPatchsetMetadata?.baseTreeId
          }
          side={fileViewSide}
          slice={authoringSlice}
          target={fileViewTarget}
        />
      </section>
    </MobileDetailsProvider>
  );
}

export { sortedPatchsets };
