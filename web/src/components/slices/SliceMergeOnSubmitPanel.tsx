import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import type { Slice } from "../../api/types";
import { useApi } from "../../api/useApi";
import { SliceNotice, SlicePanel, getErrorMessage } from "./SlicePageParts";

/**
 * Turns merge on submit on or off (design/07 section 7.4): a changeset whose
 * base went stale is merged onto the latest base when it is submitted, as a
 * new patchset that runs CI again, instead of being refused until its author
 * syncs. Changes that overlap are still refused. It saves on its own, apart
 * from the slice definition.
 */
export function SliceMergeOnSubmitPanel({ slice }: { slice: Slice }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const enabled = Boolean(slice.mergeOnSubmit);
  const [message, setMessage] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: (next: boolean) => api.setSliceMergeOnSubmit({ enabled: next, slice: slice.ref }),
    onMutate: () => setMessage(null),
    onSuccess: async (updated) => {
      setMessage(updated.mergeOnSubmit ? "Merge on submit is on." : "Merge on submit is off.");
      await queryClient.invalidateQueries({
        queryKey: ["sliceRef", slice.ref?.account, slice.ref?.slice]
      });
    }
  });

  return (
    <SlicePanel>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 max-w-2xl">
          <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Merge on submit</h2>
          <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            When a changeset is submitted after its base moved on, merge it onto the latest base
            instead of refusing it. The merge is a new patchset, so required checks run again and
            required approvals are needed again. Changes that overlap are still refused, with the
            files named, for the author to sync and resolve.
          </p>
        </div>
        <label className="inline-flex shrink-0 cursor-pointer items-center gap-2 text-sm font-medium text-zinc-800 dark:text-zinc-200">
          <input
            checked={enabled}
            className="size-4 accent-zinc-900 dark:accent-zinc-100"
            disabled={mutation.isPending || !slice.ref}
            onChange={(event) => mutation.mutate(event.target.checked)}
            type="checkbox"
          />
          Merge stale changesets on submit
        </label>
      </div>
      {mutation.isError ? (
        <div className="mt-3">
          <SliceNotice title="Could not change merge on submit" tone="error">
            {getErrorMessage(mutation.error)}
          </SliceNotice>
        </div>
      ) : message ? (
        <p className="mt-3 text-sm text-emerald-700 dark:text-emerald-300">{message}</p>
      ) : null}
    </SlicePanel>
  );
}
