import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useAuth } from "@clerk/clerk-react";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
  type PointerEvent
} from "react";

import type { Changeset, Patchset } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb, type Crumb } from "../components/Breadcrumb";
import { DiffViewer } from "../components/diff/DiffViewer";
import {
  Badge,
  Button,
  Card,
  Input,
  Surface,
  buttonClassName
} from "../components/ui";
import { cn } from "../lib/cn";
import { shortChangesetId, shortHash } from "../lib/objectId";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { displaySubmitBlockedReason } from "./stackPageUtils";

export function ChangesetDetailPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const { isLoaded, isSignedIn } = useAuth();
  const params = useParams({ strict: false }) as { id?: string };
  const changesetId = params.id ?? "";
  const [abandonReason, setAbandonReason] = useState("");
  const [actionError, setActionError] = useState("");
  const [fromPatchset, setFromPatchset] = useState("");
  const [toPatchset, setToPatchset] = useState("");

  const changesetQuery = useQuery({
    enabled: Boolean(changesetId),
    queryKey: ["changeset", changesetId],
    queryFn: () => api.getChangeset({ changesetId }),
    refetchInterval: (query) =>
      query.state.data?.status === "pending_publish" ? 2500 : false
  });

  const changeset = changesetQuery.data;
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

  if (changesetQuery.isLoading) {
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
      <div className="mb-3 hidden sm:block">
        <Breadcrumb
          items={changesetBreadcrumbItems({
            changeset,
            sliceSearch
          })}
        />
      </div>

      <HeaderCard
        abandonReason={abandonReason}
        actionBusy={actionBusy}
        actionError={actionError}
        abandonPending={abandonMutation.isPending}
        canUseReviewActions={Boolean(isLoaded && isSignedIn)}
        changeset={changeset}
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

      <DiffViewer
        diffResponse={diffQuery.data}
        error={diffQuery.error}
        isError={diffQuery.isError}
        isLoading={diffQuery.isPending}
      />
    </section>
  );
}

function HeaderCard({
  abandonPending,
  abandonReason,
  actionBusy,
  actionError,
  changeset,
  canUseReviewActions,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onMerge,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  actionError: string;
  changeset: Changeset;
  canUseReviewActions: boolean;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onMerge(): void;
  terminal: boolean;
}) {
  // On small screens the diff is the priority, so the description, base commit,
  // and review actions collapse behind a toggle. From `lg` up everything is
  // always shown in the original two-column layout (the toggle is hidden).
  const [showDetails, setShowDetails] = useState(false);
  const publishing = isPublishing(changeset.status);
  const hasExpandableContent = Boolean(
    changeset.description ||
      changeset.baseCommitId ||
      canUseReviewActions
  );

  return (
    <Card as="section" level="lowest" padding="none">
      <div className="px-3 py-2.5 md:px-5 md:py-4">
        {publishing ? (
          <Surface
            className="mb-2.5 flex items-center gap-2 px-2.5 py-1.5 text-xs font-medium text-tertiary md:mb-3 md:px-3 md:py-2 md:text-sm"
            level="high"
          >
            <span
              aria-hidden="true"
              className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-tertiary"
            />
            Publishing changeset…
          </Surface>
        ) : null}
        <div className="flex flex-col gap-2.5 lg:flex-row lg:items-start lg:justify-between lg:gap-3">
          <div className="min-w-0">
            <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-2 md:gap-3">
              <h1
                className="truncate text-xl font-semibold leading-tight text-on-surface md:text-3xl"
                title={changeset.title || "Untitled changeset"}
              >
                {changeset.title || "Untitled changeset"}
              </h1>
              {hasExpandableContent ? (
                <Button
                  aria-expanded={showDetails}
                  aria-label={showDetails ? "Hide details" : "Show details"}
                  className="shrink-0 lg:hidden"
                  onClick={() => setShowDetails((value) => !value)}
                  size="sm"
                  type="button"
                  variant="secondary"
                >
                  {showDetails ? "Hide" : canUseReviewActions ? "Actions" : "Details"}
                </Button>
              ) : null}
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-1.5 text-[11px] text-on-surface-variant md:gap-2 md:text-xs">
              <Badge
                className="max-w-[12rem] truncate font-mono md:max-w-[14rem]"
                title={changesetLabel(changeset)}
                variant="neutral"
              >
                {changesetLabel(changeset)}
              </Badge>
              <StatusBadge status={changeset.status} />
              {changeset.parentChangesetId ? (
                <Link
                  className="inline-flex max-w-[14rem] min-w-0 transition hover:opacity-85"
                  params={{
                    id:
                      shortChangesetId(changeset.parentChangesetId) ||
                      changeset.parentChangesetId
                  }}
                  title={`Base changeset ${changeset.parentChangesetId}`}
                  to="/cs/$id"
                >
                  <Badge className="min-w-0 gap-1" variant="neutral">
                    <span className="shrink-0">Base changeset</span>
                    <span className="truncate font-mono">
                      {shortChangesetId(changeset.parentChangesetId) ||
                        changeset.parentChangesetId}
                    </span>
                  </Badge>
                </Link>
              ) : null}
              <CopyLinkButton changesetId={changeset.id || ""} />
            </div>
            <ChangesetMetaLine changeset={changeset} />
            <div className={cn("lg:block", showDetails ? "block" : "hidden")}>
              {changeset.description ? (
                <p className="mt-3 max-w-3xl whitespace-pre-wrap text-sm leading-6 text-on-surface-variant md:mt-4">
                  {changeset.description}
                </p>
              ) : null}
              {changeset.baseCommitId ? (
                <p
                  className="mt-3 font-mono text-xs text-on-surface-muted md:mt-4"
                  title={changeset.baseCommitId}
                >
                  base {shortCommit(changeset.baseCommitId)}
                </p>
              ) : null}
            </div>
          </div>

          {canUseReviewActions ? (
            <div
              className={cn(
                "w-full shrink-0 lg:block lg:w-auto",
                showDetails ? "block" : "hidden"
              )}
            >
              <ReviewActions
                abandonPending={abandonPending}
                abandonReason={abandonReason}
                actionBusy={actionBusy}
                canMerge={
                  Boolean(changeset.currentPatchsetId) &&
                  isMergeableStatus(changeset.status)
                }
                mergePending={mergePending}
                onAbandon={onAbandon}
                onAbandonReasonChange={onAbandonReasonChange}
                onMerge={onMerge}
                terminal={terminal}
              />
            </div>
          ) : null}
        </div>

        {changeset.submitBlockedReason ? (
          <Surface
            className="mt-2.5 px-3 py-2 text-xs text-tertiary md:mt-3 md:text-sm"
            level="high"
          >
            {displaySubmitBlockedReason(changeset.submitBlockedReason)}
          </Surface>
        ) : null}
        {actionError ? (
          <ErrorBox className="mt-4 md:mt-5" message={actionError} />
        ) : null}
      </div>
    </Card>
  );
}

function ReviewActions({
  abandonPending,
  abandonReason,
  actionBusy,
  canMerge,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onMerge,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  canMerge: boolean;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onMerge(): void;
  terminal: boolean;
}) {
  return (
    <div className="w-full shrink-0 space-y-3 lg:w-auto lg:min-w-80">
      <div className="flex flex-wrap justify-start gap-2 lg:justify-end">
        <Button
          disabled={actionBusy || terminal || !canMerge}
          onClick={onMerge}
          type="button"
        >
          {mergePending ? "Merging..." : "Merge"}
        </Button>
      </div>

      {!terminal ? (
        <form
          className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]"
          onSubmit={onAbandon}
        >
          <label className="grid gap-1 font-label text-xs font-semibold text-on-surface-variant">
            Reason
            <Input
              className="h-9 min-w-0"
              disabled={actionBusy}
              onChange={(event) => onAbandonReasonChange(event.target.value)}
              placeholder="Optional reason"
              value={abandonReason}
            />
          </label>
          <Button
            className="self-end text-rose-700 hover:bg-rose-50"
            disabled={actionBusy}
            variant="secondary"
            type="submit"
          >
            {abandonPending ? "Abandoning..." : "Abandon"}
          </Button>
        </form>
      ) : null}
    </div>
  );
}

function PatchsetComparePanel({
  currentPatchsetId,
  fromPatchset,
  onFromPatchsetChange,
  onToPatchsetChange,
  patchsets,
  toPatchset
}: {
  currentPatchsetId?: string;
  fromPatchset: string;
  onFromPatchsetChange(value: string): void;
  onToPatchsetChange(value: string): void;
  patchsets: Patchset[];
  toPatchset: string;
}) {
  const [mobileOpen, setMobileOpen] = useState(false);
  const fromLabel = fromPatchset
    ? patchsetOptionLabel(findPatchset(patchsets, fromPatchset))
    : "Recorded base";
  const toLabel = patchsetOptionLabel(findPatchset(patchsets, toPatchset));

  return (
    <Card as="section" className="mt-2.5 md:mt-3" level="low" padding="none">
      <div className="flex items-center justify-between gap-2 px-3 py-2.5 md:px-5 md:py-3">
        <button
          aria-controls="patchset-timeline"
          aria-expanded={mobileOpen}
          className="-mx-1 flex min-w-0 flex-1 items-center gap-2 rounded-sm px-1 text-left transition hover:bg-surface-container-high lg:pointer-events-none lg:cursor-default lg:hover:bg-transparent"
          onClick={() => setMobileOpen((value) => !value)}
          type="button"
        >
          <h2 className="font-label text-[11px] font-semibold uppercase text-tertiary md:text-xs">
            Patchsets
          </h2>
          <span
            aria-hidden="true"
            className={cn(
              "inline-block shrink-0 text-[10px] text-on-surface-muted transition-transform lg:hidden",
              mobileOpen && "rotate-90"
            )}
          >
            ▶
          </span>
          <p className="min-w-0 truncate text-[11px] font-medium text-on-surface md:text-xs">
            <span className="text-on-surface-variant">{fromLabel}</span>
            <span className="mx-1 text-on-surface-muted">→</span>
            <span>{toLabel || "selected patchset"}</span>
          </p>
        </button>
      </div>
      <div
        className={cn(
          "px-3 pb-2 md:px-5 md:pb-3",
          mobileOpen ? "block" : "hidden lg:block"
        )}
        id="patchset-timeline"
      >
        <PatchsetTimeline
          currentPatchsetId={currentPatchsetId}
          fromPatchset={fromPatchset}
          onFromPatchsetChange={onFromPatchsetChange}
          onToPatchsetChange={onToPatchsetChange}
          patchsets={patchsets}
          toPatchset={toPatchset}
        />
      </div>
    </Card>
  );
}

function PatchsetTimeline({
  currentPatchsetId,
  fromPatchset,
  onFromPatchsetChange,
  onToPatchsetChange,
  patchsets,
  toPatchset
}: {
  currentPatchsetId?: string;
  fromPatchset: string;
  onFromPatchsetChange(value: string): void;
  onToPatchsetChange(value: string): void;
  patchsets: Patchset[];
  toPatchset: string;
}) {
  const selectablePatchsets = patchsets.filter((patchset) => patchset.id);
  const trackRef = useRef<HTMLDivElement | null>(null);
  const [dragging, setDragging] = useState<TimelineHandle | null>(null);
  const steps = useMemo<TimelineStep[]>(
    () => [
      { id: "", label: "Base", patchset: undefined },
      ...selectablePatchsets.map((patchset) => ({
        id: patchset.id || "",
        label: patchsetDotLabel(patchset),
        patchset
      }))
    ],
    [selectablePatchsets]
  );
  const fromIndex = timelineIndexForValue(steps, fromPatchset, 0);
  const toIndex = Math.max(1, timelineIndexForValue(steps, toPatchset, steps.length - 1));
  const maxIndex = Math.max(0, steps.length - 1);

  const applyIndex = (handle: TimelineHandle, index: number) => {
    if (!steps.length) {
      return;
    }

    const nextIndex =
      handle === "from"
        ? clamp(index, 0, maxIndex)
        : clamp(index, Math.min(1, maxIndex), maxIndex);
    const step = steps[nextIndex];

    if (handle === "from") {
      onFromPatchsetChange(step?.id || "");
      return;
    }

    if (step?.id) {
      onToPatchsetChange(step.id);
    }
  };

  const indexForPointer = (clientX: number) => {
    const track = trackRef.current;
    if (!track || maxIndex === 0) {
      return 0;
    }

    const rect = track.getBoundingClientRect();
    const ratio = clamp((clientX - rect.left) / rect.width, 0, 1);
    return Math.round(ratio * maxIndex);
  };

  const handlePointerDown = (
    handle: TimelineHandle,
    event: PointerEvent<HTMLButtonElement>
  ) => {
    event.preventDefault();
    event.currentTarget.setPointerCapture(event.pointerId);
    setDragging(handle);
    applyIndex(handle, indexForPointer(event.clientX));
  };

  const handlePointerMove = (event: PointerEvent<HTMLButtonElement>) => {
    if (!dragging) {
      return;
    }
    applyIndex(dragging, indexForPointer(event.clientX));
  };

  const stopDragging = (event: PointerEvent<HTMLButtonElement>) => {
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    setDragging(null);
  };

  const handleKeyDown = (
    handle: TimelineHandle,
    currentIndex: number,
    event: KeyboardEvent<HTMLButtonElement>
  ) => {
    if (event.key === "ArrowLeft" || event.key === "ArrowDown") {
      event.preventDefault();
      applyIndex(handle, currentIndex - 1);
    } else if (event.key === "ArrowRight" || event.key === "ArrowUp") {
      event.preventDefault();
      applyIndex(handle, currentIndex + 1);
    } else if (event.key === "Home") {
      event.preventDefault();
      applyIndex(handle, handle === "from" ? 0 : 1);
    } else if (event.key === "End") {
      event.preventDefault();
      applyIndex(handle, maxIndex);
    }
  };

  if (!selectablePatchsets.length) {
    return (
      <Surface className="mt-2 px-3 py-2 text-sm text-on-surface-variant" level="high">
        No patchsets returned.
      </Surface>
    );
  }

  return (
    <div className="mt-2">
      <div className="sr-only">
        <label>
          Diff base
          <select
            aria-label="Diff base"
            onChange={(event) => onFromPatchsetChange(event.target.value)}
            value={fromPatchset}
          >
            <option value="">Recorded base</option>
            {selectablePatchsets.map((patchset) => (
              <option key={`from-${patchsetKey(patchset)}`} value={patchset.id}>
                {patchsetOptionLabel(patchset)}
              </option>
            ))}
          </select>
        </label>
        <label>
          Target patchset
          <select
            aria-label="Target patchset"
            onChange={(event) => onToPatchsetChange(event.target.value)}
            value={toPatchset}
          >
            {selectablePatchsets.map((patchset) => (
              <option key={`to-${patchsetKey(patchset)}`} value={patchset.id}>
                {patchsetOptionLabel(patchset)}
              </option>
            ))}
          </select>
        </label>
      </div>

      <div className="relative h-20 px-4 md:px-5">
        <div className="relative h-full" ref={trackRef}>
          <div className="absolute inset-x-0 top-9 h-1 rounded-full bg-surface-container-highest" />
          <div className="absolute inset-x-0 top-6 flex items-center justify-between">
            {steps.map((step, index) => {
              const isCurrent =
                currentPatchsetId && step.id === currentPatchsetId;
              return (
                <button
                  aria-current={index === toIndex ? "true" : undefined}
                  aria-label={
                    index === 0
                      ? "Use recorded base as diff base"
                      : `Compare to ${patchsetOptionLabel(step.patchset)}`
                  }
                  className="group flex min-w-7 -translate-y-0.5 flex-col items-center gap-1 text-[10px] font-medium text-on-surface-muted md:min-w-8"
                  key={step.id || "base"}
                  onClick={() =>
                    index === 0
                      ? onFromPatchsetChange("")
                      : onToPatchsetChange(step.id)
                  }
                  type="button"
                >
                  <span
                    className={cn(
                      "rounded-full transition group-hover:bg-primary",
                      index === fromIndex || index === toIndex
                        ? "h-3.5 w-3.5 bg-primary ring-4 ring-primary/10"
                        : "h-3 w-3 bg-surface-container-highest",
                      isCurrent &&
                        !(index === fromIndex || index === toIndex) &&
                        "bg-tertiary ring-4 ring-tertiary-container"
                    )}
                    title={isCurrent ? "Current patchset" : undefined}
                  />
                  <span
                    className={cn(
                      isCurrent &&
                        !(index === fromIndex || index === toIndex) &&
                        "text-tertiary"
                    )}
                  >
                    {step.label}
                  </span>
                </button>
              );
            })}
          </div>

          <TimelineHandleButton
            handle="from"
            index={fromIndex}
            label={
              fromIndex === 0 ? "From Base" : `From ${steps[fromIndex]?.label}`
            }
            maxIndex={maxIndex}
            onKeyDown={handleKeyDown}
            onPointerDown={handlePointerDown}
            onPointerMove={handlePointerMove}
            onPointerUp={stopDragging}
            topClassName="top-0"
          />
          <TimelineHandleButton
            handle="to"
            index={toIndex}
            label={`To ${steps[toIndex]?.label || ""}`}
            maxIndex={maxIndex}
            onKeyDown={handleKeyDown}
            onPointerDown={handlePointerDown}
            onPointerMove={handlePointerMove}
            onPointerUp={stopDragging}
            topClassName="top-12"
          />
        </div>
      </div>
    </div>
  );
}

function TimelineHandleButton({
  handle,
  index,
  label,
  maxIndex,
  onKeyDown,
  onPointerDown,
  onPointerMove,
  onPointerUp,
  topClassName
}: {
  handle: TimelineHandle;
  index: number;
  label: string;
  maxIndex: number;
  onKeyDown(
    handle: TimelineHandle,
    currentIndex: number,
    event: KeyboardEvent<HTMLButtonElement>
  ): void;
  onPointerDown(
    handle: TimelineHandle,
    event: PointerEvent<HTMLButtonElement>
  ): void;
  onPointerMove(event: PointerEvent<HTMLButtonElement>): void;
  onPointerUp(event: PointerEvent<HTMLButtonElement>): void;
  topClassName: string;
}) {
  return (
    <button
      aria-label={`Drag ${handle} patchset handle`}
      aria-valuemax={maxIndex}
      aria-valuemin={handle === "from" ? 0 : Math.min(1, maxIndex)}
      aria-valuenow={index}
      className={buttonClassName({
        className: cn(
          "absolute h-auto rounded-full px-2 py-1 text-[11px] active:scale-[0.98]",
          topClassName
        ),
        size: "sm",
        variant: handle === "from" ? "secondary" : "primary"
      })}
      onKeyDown={(event) => onKeyDown(handle, index, event)}
      onPointerDown={(event) => onPointerDown(handle, event)}
      onPointerCancel={onPointerUp}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      role="slider"
      style={{
        left: timelinePosition(index, maxIndex),
        touchAction: "none",
        transform: handleTransform(index, maxIndex)
      }}
      type="button"
    >
      {label}
    </button>
  );
}

function ErrorBox({
  className,
  message
}: {
  className?: string;
  message: string;
}) {
  return (
    <Surface
      className={cn(
        "bg-rose-50 px-4 py-3 text-sm text-rose-800",
        className
      )}
      level="high"
    >
      {message}
    </Surface>
  );
}

function PageMessage({ message, title }: { message: string; title: string }) {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <Card level="low" padding="lg">
        <h1 className="text-xl font-semibold text-on-surface">
          {title}
        </h1>
        <p className="mt-2 text-sm text-on-surface-variant">{message}</p>
      </Card>
    </section>
  );
}

function ChangesetSkeleton() {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <Card level="low" padding="md">
        <div className="h-4 w-48 animate-pulse rounded-sm bg-surface-container-high" />
        <div className="mt-5 h-8 w-2/3 animate-pulse rounded-sm bg-surface-container-high" />
        <div className="mt-4 flex flex-wrap gap-2">
          <div className="h-7 w-24 animate-pulse rounded-sm bg-surface-container-high" />
          <div className="h-7 w-32 animate-pulse rounded-sm bg-surface-container-high" />
          <div className="h-7 w-20 animate-pulse rounded-sm bg-surface-container-high" />
        </div>
      </Card>
    </section>
  );
}

function StatusBadge({ status }: { status?: string }) {
  const label = humanizeStatus(status);
  return (
    <Badge
      className="md:text-xs"
      size="sm"
      title={status || "unknown"}
      variant={statusVariant(status)}
    >
      {label}
    </Badge>
  );
}

// Compact one-line summary of author and target ref. Each field renders only
// when the API returned it. The authoring slice is intentionally omitted here
// because the breadcrumb already surfaces it. On mobile this stays on a single
// truncated line so it never pushes the diff down.
function ChangesetMetaLine({ changeset }: { changeset: Changeset }) {
  const author = changeset.author;
  const ref = changeset.targetRef;
  const refLabel = ref ? compactRef(ref) : "";
  const parts: { label: string; title?: string }[] = [];
  if (author) parts.push({ label: author, title: `Author ${author}` });
  if (refLabel) parts.push({ label: refLabel, title: `Target ref ${ref}` });

  if (!parts.length) {
    return null;
  }

  return (
    <div className="mt-1 flex min-w-0 items-center gap-1.5 text-[11px] text-on-surface-muted md:mt-1.5 md:text-xs">
      {parts.map((part, index) => (
        <span className="flex min-w-0 items-center gap-1.5" key={index}>
          {index > 0 ? (
            <span aria-hidden="true" className="text-on-surface-muted">
              ·
            </span>
          ) : null}
          <span className="truncate" title={part.title}>
            {part.label}
          </span>
        </span>
      ))}
    </div>
  );
}

function compactRef(ref: string) {
  const stripped = ref.replace(/^refs\/(heads|tags|remotes)\//, "");
  return stripped || ref;
}

function humanizeStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  switch (normalized) {
    case "":
    case "draft":
      return "Draft";
    case "open":
      return "Open";
    case "pending_publish":
      return "Publishing";
    case "published":
      return "Published";
    case "submitted":
      return "Submitted";
    case "merged":
      return "Merged";
    case "abandoned":
      return "Abandoned";
    default:
      return status || "Unknown";
  }
}

function isPublishing(status?: string) {
  return (status || "").toLowerCase() === "pending_publish";
}

// Copies the short, shareable /cs/<id> URL to the clipboard so reviewers can
// paste it around. Falls back silently when the clipboard API is unavailable.
function CopyLinkButton({ changesetId }: { changesetId: string }) {
  const [copied, setCopied] = useState(false);
  const shareId = shortChangesetId(changesetId);

  if (!shareId) {
    return null;
  }

  const shareUrl =
    typeof window !== "undefined"
      ? `${window.location.origin}/cs/${shareId}`
      : `/cs/${shareId}`;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(shareUrl);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard can be blocked (insecure origin / permissions); ignore.
    }
  };

  return (
    <Button
      className="h-7 px-2 text-xs"
      onClick={copy}
      size="sm"
      title={shareUrl}
      type="button"
      variant="secondary"
    >
      <span aria-hidden="true">{copied ? "OK" : "URL"}</span>
      {copied ? "Link copied" : "Copy link"}
    </Button>
  );
}

function changesetSliceSearch(changeset: Changeset) {
  const ref = changeset.authoringSlice;
  if (!ref?.account || !ref.slice) {
    return "";
  }
  return `${ref.account}:${ref.slice}`;
}

function changesetBreadcrumbItems({
  changeset,
  sliceSearch
}: {
  changeset: Changeset;
  sliceSearch: string;
}): Crumb[] {
  const items: Crumb[] = [{ label: "Slices", to: "/slices" }];

  if (sliceSearch) {
    const routeParams = toSliceRouteParams(changeset.authoringSlice);
    items.push(
      routeParams
        ? {
            label: sliceSearch,
            params: routeParams,
            to: "/slices/$account/$slice"
          }
        : { label: sliceSearch }
    );
    items.push({
      label: `${sliceSearch} changesets`,
      search: { slice: sliceSearch },
      to: "/changesets"
    });
  }

  items.push({ label: changesetLabel(changeset) });

  return items;
}

function changesetLabel(changeset: Changeset) {
  const shortId = shortChangesetId(changeset.id || "");
  if (shortId) {
    return shortId;
  }
  if (changeset.number !== undefined && changeset.number !== "") {
    return `#${changeset.number}`;
  }
  return changeset.id || "changeset";
}

function shortCommit(commitId: string) {
  return shortHash(commitId);
}

function shortPatchsetId(patchsetId: string) {
  if (!patchsetId) {
    return "";
  }
  return patchsetId.replace(/^ps_/, "").slice(0, 12);
}

function sortedPatchsets(changeset?: Changeset) {
  return [...(changeset?.patchsets || [])].sort((left, right) => {
    const leftNumber = numericPatchsetNumber(left);
    const rightNumber = numericPatchsetNumber(right);
    if (leftNumber !== rightNumber) {
      return leftNumber - rightNumber;
    }

    return patchsetKey(left).localeCompare(patchsetKey(right));
  });
}

function numericPatchsetNumber(patchset: Patchset) {
  const number = Number(patchset.number);
  return Number.isFinite(number) ? number : Number.MAX_SAFE_INTEGER;
}

function patchsetKey(patchset: Patchset) {
  return (
    patchset.id ||
    `${patchset.number || "unknown"}-${patchset.createdAt || ""}-${
      patchset.baseCommitId || patchset.basePatchsetId || ""
    }`
  );
}

function findPatchset(patchsets: Patchset[], patchsetId: string) {
  return patchsets.find((patchset) => patchset.id === patchsetId);
}

type TimelineHandle = "from" | "to";

interface TimelineStep {
  id: string;
  label: string;
  patchset?: Patchset;
}

function patchsetOptionLabel(patchset?: Patchset) {
  if (!patchset) {
    return "";
  }
  if (patchset.number !== undefined && patchset.number !== "") {
    return `Patchset ${patchset.number}`;
  }

  const shortId = shortPatchsetId(patchset.id || "");
  return shortId ? `Patchset ${shortId}` : "Patchset";
}

function patchsetDotLabel(patchset: Patchset) {
  if (patchset.number !== undefined && patchset.number !== "") {
    return `P${patchset.number}`;
  }

  const shortId = shortPatchsetId(patchset.id || "");
  return shortId ? `P${shortId}` : "P";
}

function timelineIndexForValue(
  steps: TimelineStep[],
  value: string,
  fallback: number
) {
  if (!value) {
    return 0;
  }

  const index = steps.findIndex((step) => step.id === value);
  return index >= 0 ? index : fallback;
}

function timelinePosition(index: number, maxIndex: number) {
  if (maxIndex <= 0) {
    return "0%";
  }
  return `${(index / maxIndex) * 100}%`;
}

// Shifts the handle's box at the start/end of the track so its label cannot
// overflow the container. At the left edge the box left-aligns with the dot; at
// the right edge it right-aligns; everywhere else it centers on the dot.
function handleTransform(index: number, maxIndex: number) {
  if (index <= 0) {
    return "translateX(0)";
  }
  if (maxIndex > 0 && index >= maxIndex) {
    return "translateX(-100%)";
  }
  return "translateX(-50%)";
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value));
}

function statusVariant(status?: string) {
  switch ((status || "").toLowerCase()) {
    case "published":
    case "merged":
    case "submitted":
      return "primary";
    case "pending_publish":
    case "draft":
    case "open":
      return "tertiary";
    case "abandoned":
    default:
      return "neutral";
  }
}

// A changeset that has been submitted/published/abandoned is no longer open for
// action; merge/abandon controls hide for these.
function isTerminalStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return (
    normalized === "submitted" ||
    normalized === "pending_publish" ||
    normalized === "published" ||
    normalized === "merged" ||
    normalized === "abandoned"
  );
}

// Submit (merge) is only valid while the changeset is still open/draft.
function isMergeableStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return normalized === "" || normalized === "draft" || normalized === "open";
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}
