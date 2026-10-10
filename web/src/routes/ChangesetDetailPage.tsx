import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient
} from "@tanstack/react-query";
import {
  Link,
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
  useState,
  type FormEvent
} from "react";

import type { Changeset, FileEdit, Patchset } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { useSelection } from "../state/selection";
import { PageHeader } from "../components/PageHeader";
import { MobileDetailsProvider } from "../components/MobileDetails";
import {
  DiffViewer,
  type DiffViewerFileState
} from "../components/diff/DiffViewer";
import {
  diffFileId,
  parseDiff,
  type DiffFile,
  type FileChangeKind
} from "../components/diff/parseDiff";
import { cn } from "../lib/cn";
import { shortChangesetId } from "../lib/objectId";

import {
  ChangesetSkeleton,
  ChecksPanel,
  ConversationDrawer,
  CopyLinkButton,
  ErrorBox,
  HeaderCard,
  PageMessage,
  PatchsetComparePanel
} from "./changeset-detail";
import {
  changesetBreadcrumbItems,
  changesetSliceSearch,
  errorMessage,
  isMergeableStatus,
  isTerminalStatus,
  mergeErrorMessage
} from "./changeset-detail/status";
import { sortedPatchsets } from "./changeset-detail/patchsetUtils";

const fullDiffPathLimit = 20;
const eagerFileDiffCount = 10;

export function ChangesetDetailPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const { isLoaded, isSignedIn } = useAuth();
  const { accounts: viewerAccounts } = useSelection();
  const params = useParams({ strict: false }) as { id?: string };
  const changesetId = params.id ?? "";
  const navigate = useNavigate();
  const router = useRouter();
  const search = useSearch({ strict: false }) as {
    from?: string;
    to?: string;
    file?: string;
    conversation?: unknown;
  };
  const [abandonReason, setAbandonReason] = useState("");
  // On phones the page opens with its details (breadcrumbs, ids, patchsets,
  // checks) folded so the diff comes first; the header's Details unfolds them.
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [actionError, setActionError] = useState("");
  // The conversation drawer's open state lives in the URL so browser/mobile
  // back closes it instead of leaving the changeset detail page.
  const conversationOpen = Boolean(search.conversation);
  const [isWide, setIsWide] = useState(() =>
    typeof window === "undefined" || typeof window.matchMedia !== "function"
      ? false
      : window.matchMedia("(min-width: 1280px)").matches
  );

  useEffect(() => {
    if (
      typeof window === "undefined" ||
      typeof window.matchMedia !== "function"
    ) {
      return;
    }

    const wideQuery = window.matchMedia("(min-width: 1280px)");
    const updateIsWide = () => {
      setIsWide(wideQuery.matches);
    };

    updateIsWide();
    wideQuery.addEventListener("change", updateIsWide);

    return () => {
      wideQuery.removeEventListener("change", updateIsWide);
    };
  }, []);

  const changesetQuery = useQuery({
    enabled: Boolean(isLoaded && changesetId),
    queryKey: ["changeset", changesetId],
    queryFn: () => api.getChangeset({ changesetId }),
    refetchInterval: (query) =>
      query.state.data?.status === "pending_publish" ? 2500 : false
  });

  const changeset = changesetQuery.data;
  const authoringSlice = changeset?.authoringSlice;

  // A link made from a landed commit's id (gs shows commits shortened to 12
  // characters) still opens the changeset that landed it; switch the address
  // to the changeset's own link so that is what gets shared.
  useEffect(() => {
    const id = changeset?.id ?? "";
    const requested = changesetId.toLowerCase().replace(/^cs_/, "");
    if (!id || !requested || id.replace(/^cs_/, "").startsWith(requested)) {
      return;
    }
    void navigate({
      params: { id: shortChangesetId(id) } as never,
      replace: true,
      search: search as never,
      to: "/cs/$id"
    });
  }, [changeset?.id, changesetId]);

  const sliceChangesetsQuery = useQuery({
    enabled: Boolean(
      changeset?.id && authoringSlice?.account && authoringSlice?.slice
    ),
    queryKey: [
      "changesetsBySlice",
      authoringSlice?.account,
      authoringSlice?.slice
    ],
    queryFn: () => api.listChangesets({ authoringSlice, limit: 200, summary: true })
  });

  const dependentChangesets = useMemo(() => {
    const selfId = changeset?.id;
    if (!selfId) {
      return [] as { id: string; title: string }[];
    }
    return (sliceChangesetsQuery.data?.changesets ?? [])
      .filter((candidate) => candidate.parentChangesetId === selfId && candidate.id)
      .map((candidate) => ({
        id: candidate.id as string,
        title: candidate.title ?? ""
      }));
  }, [sliceChangesetsQuery.data?.changesets, changeset?.id]);

  const canonicalChangesetId = changeset?.id || changesetId;
  const sliceSearch = changeset ? changesetSliceSearch(changeset) : "";
  const patchsets = useMemo(() => sortedPatchsets(changeset), [changeset]);
  const patchsetIds = useMemo(
    () =>
      new Set(
        patchsets
          .map((patchset) => patchset.id)
          .filter((id): id is string => Boolean(id))
      ),
    [patchsets]
  );
  const defaultToPatchset =
    changeset?.currentPatchsetId || patchsets[patchsets.length - 1]?.id || "";
  // The compare handles live in the URL (?from=&to=) so a selection is
  // shareable and survives reloads. Fall back to the recorded base / current
  // patchset when a param is missing or no longer points at a real patchset.
  const fromPatchset =
    search.from && patchsetIds.has(search.from) ? search.from : "";
  const selectedToPatchset =
    search.to && patchsetIds.has(search.to) ? search.to : defaultToPatchset;

  const selectedFromPatchset = useMemo(
    () => patchsets.find((patchset) => patchset.id === fromPatchset),
    [fromPatchset, patchsets]
  );
  const selectedToPatchsetMetadata = useMemo(
    () => patchsets.find((patchset) => patchset.id === selectedToPatchset),
    [patchsets, selectedToPatchset]
  );
  const changedPaths = useMemo(
    () =>
      changedPathsForDiff(
        selectedFromPatchset,
        selectedToPatchsetMetadata
      ),
    [selectedFromPatchset, selectedToPatchsetMetadata]
  );
  const changedPathSet = useMemo(() => new Set(changedPaths), [changedPaths]);
  const fileMetadataByPath = useMemo(
    () =>
      new Map(
        changedPaths.map((path) => [
          path,
          fileMetadataForPath(
            path,
            selectedFromPatchset,
            selectedToPatchsetMetadata
          )
        ])
      ),
    [changedPaths, selectedFromPatchset, selectedToPatchsetMetadata]
  );
  const usesLazyFileDiffs = changedPaths.length > fullDiffPathLimit;
  const comparisonKey = `${canonicalChangesetId}\0${fromPatchset}\0${selectedToPatchset}`;
  const [requestedFilePaths, setRequestedFilePaths] = useState<{
    comparisonKey: string;
    paths: Set<string>;
  }>(() => ({ comparisonKey: "", paths: new Set() }));
  const demandedFilePaths =
    requestedFilePaths.comparisonKey === comparisonKey
      ? requestedFilePaths.paths
      : undefined;

  const requestFileDiff = useCallback(
    (path: string) => {
      if (!usesLazyFileDiffs || !changedPathSet.has(path)) {
        return;
      }
      setRequestedFilePaths((current) => {
        const currentPaths =
          current.comparisonKey === comparisonKey
            ? current.paths
            : new Set<string>();
        if (currentPaths.has(path)) {
          return current;
        }
        const nextPaths = new Set(currentPaths);
        nextPaths.add(path);
        return { comparisonKey, paths: nextPaths };
      });
    },
    [changedPathSet, comparisonKey, usesLazyFileDiffs]
  );

  const updateCompareSearch = useCallback(
    (next: { from?: string; to?: string }) => {
      void navigate({
        params: { id: changesetId } as never,
        replace: true,
        search: ((prev: Record<string, unknown>) => {
          const merged: Record<string, unknown> = { ...prev, ...next };
          // Keep the recorded base / default target out of the URL.
          for (const key of ["from", "to"] as const) {
            if (!merged[key]) {
              delete merged[key];
            }
          }
          return merged;
        }) as never,
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

  const diffQuery = useQuery({
    enabled: Boolean(
      changeset && canonicalChangesetId && !usesLazyFileDiffs
    ),
    queryKey: [
      "changesetDiff",
      canonicalChangesetId,
      fromPatchset,
      selectedToPatchset
    ],
    queryFn: () =>
      api.diffChangeset({
        changesetId: canonicalChangesetId,
        fromPatchset: fromPatchset || undefined,
        toPatchset: selectedToPatchset || undefined
      })
  });

  const fileDiffQueries = useQueries({
    queries: usesLazyFileDiffs
      ? changedPaths.map((path, index) => ({
          enabled: Boolean(
            canonicalChangesetId &&
              (index < eagerFileDiffCount ||
                demandedFilePaths?.has(path) ||
                search.file === path)
          ),
          queryKey: [
            "changesetFileDiff",
            canonicalChangesetId,
            fromPatchset,
            selectedToPatchset,
            path
          ],
          queryFn: async () => {
            const response = await api.diffChangeset({
              changesetId: canonicalChangesetId,
              fromPatchset: fromPatchset || undefined,
              paths: [path],
              toPatchset: selectedToPatchset || undefined
            });
            return {
              file: diffFileForResponse(
                path,
                fileMetadataByPath.get(path)?.changeKind,
                response.diff
              )
            };
          },
          retry: false,
          staleTime: Infinity
        }))
      : []
  });

  const fileStates = useMemo<DiffViewerFileState[] | undefined>(() => {
    if (!usesLazyFileDiffs) {
      return undefined;
    }

    return changedPaths.map((path, index) => {
      const query = fileDiffQueries[index];
      const metadata = fileMetadataByPath.get(path) ?? {};

      if (query?.data) {
        return {
          ...metadata,
          file: query.data.file,
          path,
          status: "loaded"
        };
      }
      if (query?.isError) {
        return {
          ...metadata,
          error: query.error,
          path,
          status: "error"
        };
      }
      if (query?.isFetching) {
        return { ...metadata, path, status: "loading" };
      }
      return { ...metadata, path, status: "pending" };
    });
  }, [
    changedPaths,
    fileMetadataByPath,
    fileDiffQueries,
    usesLazyFileDiffs
  ]);

  const retryFileDiff = useCallback(
    (path: string) => {
      requestFileDiff(path);
      const index = changedPaths.indexOf(path);
      if (index >= 0) {
        void fileDiffQueries[index]?.refetch();
      }
    },
    [changedPaths, fileDiffQueries, requestFileDiff]
  );

  const invalidateChangeset = async () => {
    await queryClient.invalidateQueries({
      queryKey: ["changeset", changesetId]
    });
  };
  const openConversation = useCallback(() => {
    void navigate({
      params: { id: changesetId } as never,
      search: ((prev: Record<string, unknown>) => ({
        ...prev,
        conversation: "1"
      })) as never,
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

  const mergeMutation = useMutation({
    mutationFn: async () => {
      if (!canonicalChangesetId) {
        throw new Error("This changeset did not return an id.");
      }
      if (!changeset?.currentPatchsetId) {
        throw new Error("This changeset has no current patchset to merge.");
      }

      return api.submitChangeset({
        changesetId: canonicalChangesetId,
        expectedCurrentPatchsetId: changeset.currentPatchsetId
      });
    },
    onError: (error) => setActionError(mergeErrorMessage(error)),
    onMutate: () => setActionError(""),
    onSuccess: async () => {
      setActionError("");
      await invalidateChangeset();
    }
  });

  const abandonMutation = useMutation({
    mutationFn: async () => {
      if (!canonicalChangesetId) {
        throw new Error("This changeset did not return an id.");
      }

      return api.abandonChangeset({
        changesetId: canonicalChangesetId,
        reason: abandonReason.trim()
      });
    },
    onError: (error) => setActionError(errorMessage(error)),
    onMutate: () => setActionError(""),
    onSuccess: async () => {
      setActionError("");
      setAbandonReason("");
      await invalidateChangeset();
    }
  });

  const submitAbandon = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    abandonMutation.mutate();
  };

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
  const actionBusy = mergeMutation.isPending || abandonMutation.isPending;
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
          abandonPending={abandonMutation.isPending}
          canUseReviewActions={Boolean(isLoaded && isSignedIn)}
          changeset={changeset}
          dependentChangesets={dependentChangesets}
          mergePending={mergeMutation.isPending}
          onAbandon={submitAbandon}
          onAbandonReasonChange={setAbandonReason}
          onMerge={() => mergeMutation.mutate()}
          patchsetCompare={
            <PatchsetComparePanel
              conversationOpen={conversationOpen}
              currentPatchsetId={changeset.currentPatchsetId}
              fromPatchset={fromPatchset}
              onFromPatchsetChange={handleFromPatchsetChange}
              onToPatchsetChange={handleToPatchsetChange}
              onToggleConversation={toggleConversation}
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
            />
          </div>
          <ConversationDrawer
            docked={isWide}
            enabled={Boolean(isLoaded)}
            fromPatchsetId={fromPatchset}
            onClose={closeConversation}
            open={conversationOpen}
            patchsets={patchsets}
            selectedPatchsetId={selectedToPatchset}
          />
        </div>
      </section>
    </MobileDetailsProvider>
  );
}

export { sortedPatchsets };

const messageLinkClass =
  "rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-1.5 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950";

// Why a changeset did not load, in terms of what the viewer can do: a wrong
// id, a private slice to sign in to, or a slice they are not a member of.
function ChangesetLoadError({
  changesetId,
  error,
  signedIn
}: {
  changesetId: string;
  error: unknown;
  signedIn: boolean;
}) {
  const status = (error as { status?: number } | null)?.status;
  if (status === 404) {
    return (
      <PageMessage
        message="Nothing matches this id, as a changeset or as a commit a changeset landed. Check the link: gs prints changeset ids as 10 hex characters, with a view: link next to them."
        title={`No changeset ${changesetId}`}
      />
    );
  }
  if (status === 401 || (status === 403 && !signedIn)) {
    return (
      <PageMessage
        message="This changeset is in a private slice. Sign in to see it if you are a member."
        title="Sign in to see this changeset"
      >
        <Link className={messageLinkClass} to="/login">
          Sign in
        </Link>
      </PageMessage>
    );
  }
  if (status === 403) {
    return (
      <PageMessage
        message="It is in a private slice you are not a member of. Ask the slice's owner to add you. If an agent of yours made it, claim the agent to see its slices."
        title="You can't see this changeset"
      >
        <Link className={messageLinkClass} to="/claims">
          Claim agents
        </Link>
      </PageMessage>
    );
  }
  return (
    <PageMessage message={errorMessage(error)} title="Unable to load changeset" />
  );
}

function changedPathsForDiff(from?: Patchset, to?: Patchset) {
  const paths = new Set<string>();
  [from, to].forEach((patchset) => {
    patchset?.fileEdits?.forEach((edit) => {
      if (edit.path) {
        paths.add(edit.path);
      }
      if (edit.oldPath) {
        paths.add(edit.oldPath);
      }
    });
  });
  return Array.from(paths).sort();
}

function fileMetadataForPath(
  path: string,
  from?: Patchset,
  to?: Patchset
): { changeKind?: FileChangeKind; oldPath?: string } {
  const edit =
    findEditForPath(to?.fileEdits, path) ??
    findEditForPath(from?.fileEdits, path);
  if (!edit) {
    return {};
  }

  return {
    changeKind: changeKindForEdit(edit),
    oldPath:
      edit.op === "rename" && edit.path === path ? edit.oldPath : undefined
  };
}

function findEditForPath(edits: FileEdit[] | undefined, path: string) {
  return edits?.find((edit) => edit.path === path || edit.oldPath === path);
}

function changeKindForEdit(edit: FileEdit): FileChangeKind | undefined {
  switch (edit.op) {
    case "delete":
      return "deleted";
    case "rename":
      return "renamed";
    default:
      return undefined;
  }
}

function diffFileForResponse(
  path: string,
  metadataKind: FileChangeKind | undefined,
  diff: string | undefined
): DiffFile {
  const parsed = parseDiff(diff ?? "", [path])[0];
  if (!parsed) {
    return {
      additions: 0,
      changeKind: metadataKind ?? "modified",
      deletions: 0,
      id: diffFileId(path),
      lines: [],
      path,
      rows: []
    };
  }

  return {
    ...parsed,
    changeKind: parsed.changeKind,
    id: diffFileId(path),
    path
  };
}
