import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import type { Slice } from "../../api/types";
import { useApi } from "../../api/useApi";
import { SliceNotice, SlicePanel, getErrorMessage } from "./SlicePageParts";

// The server's limit (service.maxSliceDescriptionRunes).
export const MAX_SLICE_DESCRIPTION = 500;

/**
 * Edits a slice's description: a short note on its page, such as what it
 * holds or which project it mirrors. It saves on its own, apart from the
 * slice definition, because it is not part of the definition.
 */
export function SliceDescriptionPanel({ slice }: { slice: Slice }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const saved = slice.description ?? "";
  const [draft, setDraft] = useState(saved);
  const [message, setMessage] = useState<string | null>(null);

  useEffect(() => {
    setDraft(saved);
  }, [saved]);

  const mutation = useMutation({
    mutationFn: (description: string) =>
      api.setSliceDescription({ description, slice: slice.ref }),
    onMutate: () => setMessage(null),
    onSuccess: async (updated) => {
      setMessage(updated.description ? "Description saved." : "Description cleared.");
      await queryClient.invalidateQueries({
        queryKey: ["sliceRef", slice.ref?.account, slice.ref?.slice]
      });
    }
  });

  const length = [...draft.trim()].length;
  const tooLong = length > MAX_SLICE_DESCRIPTION;
  const unchanged = draft.trim() === saved;

  return (
    <SlicePanel>
      <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Description</h2>
      <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
        Shown on the slice&apos;s page: what it holds, or which project it mirrors and who to credit.
      </p>
      <label className="mt-3 grid gap-1.5 text-sm font-medium text-zinc-800 dark:text-zinc-200">
        <span className="sr-only">Description</span>
        <textarea
          aria-label="Description"
          className="min-h-24 w-full rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm text-zinc-950 dark:text-zinc-50 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700"
          disabled={mutation.isPending}
          onChange={(event) => setDraft(event.target.value)}
          placeholder="A mirror of example/project by its authors. File issues upstream."
          value={draft}
        />
      </label>
      <div className="mt-2 flex flex-wrap items-center gap-3">
        <button
          className="rounded-md bg-zinc-950 dark:bg-zinc-100 px-3 py-1.5 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-200 dark:disabled:bg-zinc-700 disabled:text-slate-500 dark:disabled:text-zinc-400"
          disabled={mutation.isPending || tooLong || unchanged || !slice.ref}
          onClick={() => mutation.mutate(draft.trim())}
          type="button"
        >
          {mutation.isPending ? "Saving..." : "Save description"}
        </button>
        <span className={tooLong ? "text-xs text-red-700 dark:text-red-400" : "text-xs text-slate-500 dark:text-zinc-400"}>
          {length}/{MAX_SLICE_DESCRIPTION}
        </span>
      </div>
      {mutation.error ? (
        <div className="mt-3">
          <SliceNotice title="Could not save the description" tone="error">
            {getErrorMessage(mutation.error)}
          </SliceNotice>
        </div>
      ) : null}
      {message ? (
        <div className="mt-3">
          <SliceNotice title={message} tone="success">
            The slice&apos;s page shows it now.
          </SliceNotice>
        </div>
      ) : null}
    </SlicePanel>
  );
}

const URL_PATTERN = /(https?:\/\/[^\s<>"')]+)/g;

/** A slice's description as text, with its web addresses as links. */
export function SliceDescription({ text, className }: { text: string; className?: string }) {
  const parts = text.split(URL_PATTERN);
  return (
    <p className={className}>
      {parts.map((part, index) =>
        index % 2 === 1 ? (
          <a
            className="underline decoration-slate-400 underline-offset-2 hover:text-zinc-950 dark:hover:text-zinc-50"
            href={part}
            key={index}
            rel="nofollow noopener noreferrer"
            target="_blank"
          >
            {part}
          </a>
        ) : (
          part
        )
      )}
    </p>
  );
}
