import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { useAuth } from "@clerk/tanstack-react-start";
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type FormEvent
} from "react";

import type { Changeset } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import { DiffViewer } from "../components/diff/DiffViewer";
import { cn } from "../lib/cn";

import {
  ChangesetSkeleton,
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
  isTerminalStatus
} from "./changeset-detail/status";
import { sortedPatchsets } from "./changeset-detail/patchsetUtils";

export function ChangesetDetailPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const { isLoaded, isSignedIn } = useAuth();
  const params = useParams({ strict: false }) as { id?: string };
  const changesetId = params.id ?? "";
  const [abandonReason, setAbandonReason] = useState("");
  const [actionError, setActionError] = useState("");
  const [conversationOpen, setConversationOpen] = useState(false);
  const [fromPatchset, setFromPatchset] = useState("");
  const [isWide, setIsWide] = useState(() =>
    typeof window === "undefined" || typeof window.matchMedia !== "function"
      ? false
      : window.matchMedia("(min-width: 1280px)").matches
  );
  const [toPatchset, setToPatchset] = useState("");

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

  const sliceChangesetsQuery = useQuery({
    enabled: Boolean(
      changeset?.id && authoringSlice?.account && authoringSlice?.slice
    ),
    queryKey: [
      "changesetsBySlice",
      authoringSlice?.account,
      authoringSlice?.slice
    ],
    queryFn: () => api.listChangesets({ authoringSlice, limit: 200 })
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
  const patchsetIdsKey = patchsets.map((patchset) => patchset.id || "").join("|");
  const selectedToPatchset =
    toPatchset ||
    changeset?.currentPatchsetId ||
    patchsets[patchsets.length - 1]?.id ||
    "";

  useEffect(() => {
    if (!changeset) {
      setFromPatchset("");
      setToPatchset("");
      return;
    }

    const ids = new Set(
      patchsets
        .map((patchset) => patchset.id)
        .filter((id): id is string => Boolean(id))
    );
    const defaultTo =
      changeset.currentPatchsetId || patchsets[patchsets.length - 1]?.id || "";

    setFromPatchset((current) =>
      current === "" || ids.has(current) ? current : ""
    );
    setToPatchset((current) => (current && ids.has(current) ? current : defaultTo));
  }, [changeset, patchsetIdsKey, patchsets]);

  const diffQuery = useQuery({
    enabled: Boolean(changeset && canonicalChangesetId),
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

  const invalidateChangeset = async () => {
    await queryClient.invalidateQueries({
      queryKey: ["changeset", changesetId]
    });
  };
  const closeConversation = useCallback(() => setConversationOpen(false), []);

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
    onError: (error) => setActionError(errorMessage(error)),
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
      <PageMessage
        title="Unable to load changeset"
        message={errorMessage(changesetQuery.error)}
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
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        breadcrumb={
          <Breadcrumb
            items={changesetBreadcrumbItems({
              changeset,
              sliceSearch
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
        terminal={terminal}
      />

      <PatchsetComparePanel
        currentPatchsetId={changeset.currentPatchsetId}
        fromPatchset={fromPatchset}
        onFromPatchsetChange={setFromPatchset}
        onToPatchsetChange={setToPatchset}
        patchsets={patchsets}
        toPatchset={selectedToPatchset}
      />

      <div className={cn(isWide && conversationOpen && "flex items-start gap-3")}>
        <div className={cn(isWide && conversationOpen && "min-w-0 flex-1")}>
          <DiffViewer
            diffResponse={diffQuery.data}
            error={diffQuery.error}
            isError={diffQuery.isError}
            isLoading={diffQuery.isPending}
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

      {!conversationOpen ? (
        <button
          aria-expanded={conversationOpen}
          className="fixed bottom-4 right-4 z-30 inline-flex h-10 items-center gap-2 rounded-full border border-slate-200 bg-zinc-950 px-4 text-sm font-medium text-white shadow-lg shadow-slate-900/20 transition hover:bg-zinc-800 active:scale-[0.98]"
          onClick={() => setConversationOpen(true)}
          type="button"
        >
          <span aria-hidden="true">💬</span>
          <span>Conversation</span>
        </button>
      ) : null}
    </section>
  );
}

export { sortedPatchsets };
