import {
  type UseQueryResult,
  useMutation,
  useQuery,
  useQueryClient
} from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";

import type {
  ChangesetStack,
  ChangesetStackEntry,
  DiffChangesetResponse,
  TreeEntry
} from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { normalizeRepositoryPath } from "../components/source/sourceUtils";
import {
  SliceLoadingBlock,
  SliceNotice
} from "../components/slices/SlicePageParts";
import { cn } from "../lib/cn";
import {
  changedPathCount,
  childCountMap,
  conflictCount,
  currentPatchset,
  currentPatchsetNumber,
  displaySubmitBlockedReason,
  entryByChangesetId,
  entryDepth,
  entryLabel,
  entryTitle,
  formatCommit,
  formatTimestamp,
  getErrorMessage,
  isTerminalChangesetStatus,
  parentEntry,
  primaryButtonClass,
  secondaryButtonClass,
  shortStackId,
  sliceRefLabel,
  sortedStackEntries,
  stackDisplayName,
  StackStatusBadge
} from "./stackPageUtils";
import { hasInProgressEntry, inputClass } from "./stack-detail/stackUtils";
import { StackHeader } from "./stack-detail/StackHeader";
import { StackEntryList } from "./stack-detail/StackEntryList";
import { SelectedEntryDetail } from "./stack-detail/SelectedEntryDetail";
import { StackTreeControls } from "./stack-detail/StackTreeControls";
import { StackMessage } from "./stack-detail/StackMessage";

interface StackParams {
  id?: string;
}

interface StackSearch {
  entry?: unknown;
}

export function StackDetailPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as StackParams;
  const search = useSearch({ strict: false }) as StackSearch;
  const stackId = params.id ?? "";
  const requestedEntryId = typeof search.entry === "string" ? search.entry : "";
  const [actionMessage, setActionMessage] = useState("");
  const [actionError, setActionError] = useState("");

  const stackQuery = useQuery({
    enabled: Boolean(stackId),
    queryKey: ["stack", stackId],
    queryFn: () => api.getStack({ stackId }),
    refetchInterval: (query) =>
      hasInProgressEntry(query.state.data) ? 2500 : false
  });

  const stack = stackQuery.data;
  const entries = useMemo(() => sortedStackEntries(stack), [stack]);
  const defaultEntryId =
    stack?.activeEntryId || stack?.rootEntryId || entries[0]?.changesetId || "";
  const selectedEntry =
    entryByChangesetId(entries, requestedEntryId || defaultEntryId) ??
    entries[0] ??
    null;
  const selectedChangesetId = selectedEntry?.changesetId ?? "";

  const diffQuery = useQuery({
    enabled: Boolean(selectedChangesetId),
    queryKey: ["stackEntryDiff", stackId, selectedChangesetId],
    queryFn: () => api.diffChangeset({ changesetId: selectedChangesetId })
  });

  const invalidateStack = async () => {
    await queryClient.invalidateQueries({ queryKey: ["stack", stackId] });
  };

  const selectEntry = (entryId: string) => {
    void navigate({
      params: { id: stackId },
      search: entryId ? ({ entry: entryId } as never) : ({} as never),
      to: "/dependencies/$id"
    });
  };

  if (!stackId) {
    return <StackMessage message="No dependency tree id was provided." title="Missing dependency tree" />;
  }

  if (stackQuery.isLoading) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
        <SliceLoadingBlock />
      </section>
    );
  }

  if (stackQuery.isError) {
    return (
      <StackMessage
        message={getErrorMessage(stackQuery.error)}
        title="Unable to load dependency tree"
      />
    );
  }

  if (!stack) {
    return <StackMessage message="The API returned no dependency tree." title="Dependency tree not found" />;
  }

  const sliceLabel = sliceRefLabel(stack.authoringSlice);

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="mb-4">
        <Breadcrumb
          items={[
            { label: "Dependencies", to: "/dependencies" },
            ...(sliceLabel
              ? [
                  {
                    label: sliceLabel,
                    search: { slice: sliceLabel },
                    to: "/dependencies"
                  }
                ]
              : []),
            { label: shortStackId(stack.id) || stackDisplayName(stack) }
          ]}
        />
      </div>

      <StackHeader stack={stack} />

      {actionMessage ? (
        <div className="mt-5">
          <SliceNotice title="Dependencies updated" tone="success">
            {actionMessage}
          </SliceNotice>
        </div>
      ) : null}
      {actionError ? (
        <div className="mt-5">
          <SliceNotice title="Dependency action failed" tone="error">
            {actionError}
          </SliceNotice>
        </div>
      ) : null}

      <div className="mt-6 grid gap-6 xl:grid-cols-[minmax(0,1.35fr)_minmax(24rem,0.85fr)]">
        <div className="min-w-0 space-y-6">
          <StackEntryList
            entries={entries}
            onSelect={selectEntry}
            selectedEntryId={selectedChangesetId}
            stackId={stackId}
          />
          <SelectedEntryDetail
            diffQuery={diffQuery}
            entries={entries}
            entry={selectedEntry}
            stack={stack}
          />
        </div>

        <StackTreeControls
          entries={entries}
          onError={setActionError}
          onMessage={setActionMessage}
          onRefresh={invalidateStack}
          onSelect={selectEntry}
          selectedEntry={selectedEntry}
          stack={stack}
        />
      </div>
    </section>
  );
}