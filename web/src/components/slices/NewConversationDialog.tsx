import { FormEvent, useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { Conversation, Slice, SliceRef } from "../../api/types";
import { useApi } from "../../api/useApi";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
import { useSelection } from "../../state/selection";
import { Popup } from "../Popup";
import { SliceNotice, getErrorMessage, sliceDisplayName } from "./SlicePageParts";

interface NewConversationDialogProps {
  open: boolean;
  onClose: () => void;
  onCreated?: (conversation: Conversation) => void;
}

interface SliceOption {
  label: string;
  value: string;
}

interface CreateInput {
  daemonId: string;
  slice: SliceRef;
  title: string;
}

const PAGE_SIZE = 100;

export function NewConversationDialog({
  onClose,
  onCreated,
  open
}: NewConversationDialogProps) {
  const api = useApi();
  const queryClient = useQueryClient();
  const selection = useSelection();
  const account = selection.account.trim();

  const [selectedDaemonId, setSelectedDaemonId] = useState("");
  const [selectedSliceKey, setSelectedSliceKey] = useState("");
  const [title, setTitle] = useState("");

  const daemonsQuery = useQuery({
    enabled: open,
    queryKey: ["agentDaemons"],
    queryFn: async () => (await api.listDaemons({})).daemons ?? []
  });

  const slicesQuery = useQuery({
    enabled: open && account.length > 0,
    queryKey: ["slices", account],
    queryFn: async () => {
      const slices: Slice[] = [];
      let cursor = "";

      do {
        const response = await api.listSlices({
          account,
          cursor,
          pageSize: PAGE_SIZE
        });

        slices.push(...(response.slices ?? []));
        cursor = response.nextCursor ?? "";
      } while (cursor);

      return slices;
    }
  });

  const onlineDaemons = useMemo(
    () =>
      (daemonsQuery.data ?? []).filter((daemon) => daemon.status === "online"),
    [daemonsQuery.data]
  );

  const sliceOptions = useMemo<SliceOption[]>(
    () =>
      (slicesQuery.data ?? []).flatMap((slice) => {
        const routeParams = toSliceRouteParams(slice.ref);
        if (!routeParams) {
          return [];
        }

        return [
          {
            label: sliceDisplayName(slice),
            value: `${routeParams.account}:${routeParams.slice}`
          }
        ];
      }),
    [slicesQuery.data]
  );

  const createMutation = useMutation({
    mutationFn: (input: CreateInput) =>
      api.createConversation({
        daemonId: input.daemonId,
        slice: input.slice,
        title: input.title || undefined
      }),
    onSuccess: (conversation, input) => {
      const sliceKey = `${input.slice.account ?? ""}:${input.slice.slice ?? ""}`;
      void queryClient.invalidateQueries({ queryKey: ["recentConversations"] });
      void queryClient.invalidateQueries({ queryKey: ["allConversations"] });
      void queryClient.invalidateQueries({
        queryKey: ["sliceConversations", sliceKey]
      });
      onCreated?.(conversation);
      setTitle("");
      onClose();
    }
  });

  useEffect(() => {
    if (!open) {
      return;
    }
    createMutation.reset();
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }
    setSelectedDaemonId((current) =>
      onlineDaemons.some((daemon) => daemon.id === current)
        ? current
        : (onlineDaemons[0]?.id ?? "")
    );
  }, [onlineDaemons, open]);

  useEffect(() => {
    if (!open) {
      return;
    }
    setSelectedSliceKey((current) =>
      sliceOptions.some((slice) => slice.value === current)
        ? current
        : (sliceOptions[0]?.value ?? "")
    );
  }, [open, sliceOptions]);

  function createConversation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const slice = parseSliceOptionValue(selectedSliceKey);
    if (!selectedDaemonId || !slice) {
      return;
    }

    createMutation.mutate({
      daemonId: selectedDaemonId,
      slice,
      title: title.trim()
    });
  }

  const createError = createMutation.error
    ? getErrorMessage(createMutation.error)
    : "";
  const isCreating = createMutation.isPending;
  const isLoadingSlices =
    selection.isLoading || (open && account.length > 0 && slicesQuery.isPending);
  const isLoadingDaemons = open && daemonsQuery.isPending;
  const hasNoSlices =
    !selection.isLoading &&
    account.length > 0 &&
    !isLoadingSlices &&
    sliceOptions.length === 0;
  const hasNoOnlineDaemons = !isLoadingDaemons && onlineDaemons.length === 0;
  const canSubmit =
    !isCreating &&
    Boolean(selectedDaemonId) &&
    Boolean(selectedSliceKey) &&
    sliceOptions.some((slice) => slice.value === selectedSliceKey) &&
    onlineDaemons.length > 0;

  return (
    <Popup onClose={onClose} open={open} title="New conversation">
      <form className="grid gap-3" onSubmit={createConversation}>
        <label className="grid gap-1.5 text-sm font-medium text-zinc-800 dark:text-zinc-200">
          Slice
          <select
            className="min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm text-zinc-950 dark:text-zinc-50 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700 disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-500 dark:disabled:text-zinc-400"
            disabled={isLoadingSlices || sliceOptions.length === 0}
            onChange={(event) => setSelectedSliceKey(event.target.value)}
            value={selectedSliceKey}
          >
            {isLoadingSlices ? (
              <option value="">Loading slices...</option>
            ) : sliceOptions.length ? (
              sliceOptions.map((slice) => (
                <option key={slice.value} value={slice.value}>
                  {slice.label}
                </option>
              ))
            ) : (
              <option value="">No slices</option>
            )}
          </select>
        </label>

        {selection.error ? (
          <SliceNotice title="Could not load your home account" tone="error">
            {getErrorMessage(selection.error)}
          </SliceNotice>
        ) : !selection.isLoading && !account ? (
          <SliceNotice title="Select an account">
            Your signed-in session did not return a home account.
          </SliceNotice>
        ) : slicesQuery.isError ? (
          <SliceNotice title="Could not load slices" tone="error">
            {getErrorMessage(slicesQuery.error)}
          </SliceNotice>
        ) : hasNoSlices ? (
          <SliceNotice title="Create a slice first">
            Conversations need a slice. Create a slice before starting a
            conversation.
          </SliceNotice>
        ) : null}

        <label className="grid gap-1.5 text-sm font-medium text-zinc-800 dark:text-zinc-200">
          Agent daemon
          <select
            className="min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm text-zinc-950 dark:text-zinc-50 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700 disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-500 dark:disabled:text-zinc-400"
            disabled={isLoadingDaemons || onlineDaemons.length === 0}
            onChange={(event) => setSelectedDaemonId(event.target.value)}
            value={selectedDaemonId}
          >
            {isLoadingDaemons ? (
              <option value="">Loading daemons...</option>
            ) : onlineDaemons.length ? (
              onlineDaemons.map((daemon) => (
                <option key={daemon.id ?? daemon.name} value={daemon.id ?? ""}>
                  {daemon.name || daemon.id}
                </option>
              ))
            ) : (
              <option value="">No online daemons</option>
            )}
          </select>
        </label>

        {daemonsQuery.isError ? (
          <SliceNotice title="Could not load agent daemons" tone="error">
            {getErrorMessage(daemonsQuery.error)}
          </SliceNotice>
        ) : hasNoOnlineDaemons ? (
          <div className="rounded-md border border-dashed border-slate-300 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-950 p-4 text-sm text-slate-700 dark:text-zinc-300">
            <p className="font-semibold text-zinc-950 dark:text-zinc-50">No online daemons</p>
            <p className="mt-2 leading-6">
              Run{" "}
              <code className="rounded bg-white dark:bg-zinc-900 px-1.5 py-0.5 font-mono text-xs text-slate-800 dark:text-zinc-200">
                gs agent start
              </code>{" "}
              in an empty directory to make an agent available here.
            </p>
          </div>
        ) : null}

        <label className="grid gap-1.5 text-sm font-medium text-zinc-800 dark:text-zinc-200">
          Title
          <input
            className="min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm text-zinc-950 dark:text-zinc-50 outline-none transition placeholder:text-slate-400 dark:placeholder:text-zinc-500 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700"
            onChange={(event) => setTitle(event.target.value)}
            placeholder="Optional"
            value={title}
          />
        </label>

        {createError ? (
          <p className="text-sm text-rose-700 dark:text-rose-300">{createError}</p>
        ) : null}

        <div className="mt-1 flex justify-end gap-2">
          <button
            className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
            onClick={onClose}
            type="button"
          >
            Cancel
          </button>
          <button
            className="rounded-md bg-zinc-950 dark:bg-zinc-100 px-3 py-2 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300 dark:disabled:bg-zinc-600"
            disabled={!canSubmit}
            type="submit"
          >
            {isCreating ? "Creating..." : "Create conversation"}
          </button>
        </div>
      </form>
    </Popup>
  );
}

function parseSliceOptionValue(value: string): SliceRef | null {
  const separator = value.indexOf(":");
  if (separator <= 0 || separator === value.length - 1) {
    return null;
  }

  return {
    account: value.slice(0, separator),
    slice: value.slice(separator + 1)
  };
}
