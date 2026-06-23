import type { ChangesetStack, ChangesetStackEntry } from "../../api/types";
import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import {
  currentPatchset,
  currentPatchsetNumber,
  entryByChangesetId,
  entryLabel,
  getErrorMessage
} from "../stackPageUtils";
import { normalizeRepositoryPath } from "../../components/source/sourceUtils";
import { inputClass } from "./stackUtils";
import { utf8ToBase64 } from "./stackUtils";

export function AddPatchsetPanel({
  entries,
  onError,
  onMessage,
  onRefresh,
  selectedEntry,
  stack
}: {
  entries: ChangesetStackEntry[];
  onError(message: string): void;
  onMessage(message: string): void;
  onRefresh(): Promise<void>;
  selectedEntry: ChangesetStackEntry | null;
  stack: ChangesetStack;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [filePath, setFilePath] = useState("");
  const [fileContent, setFileContent] = useState("");
  const stackId = stack.id || "";

  const patchsetMutation = useMutation({
    mutationFn: async () => {
      if (!selectedEntry?.changesetId || !selectedEntry.changeset) {
        throw new Error("Choose a changeset before adding a patchset.");
      }
      if (!stack.authoringSlice?.account || !stack.authoringSlice.slice) {
        throw new Error("This dependency tree did not return an authoring slice.");
      }

      const trimmedPath = filePath.trim();
      if (!trimmedPath) {
        throw new Error("Enter a repository path.");
      }

      const current = currentPatchset(selectedEntry.changeset);
      const parent = selectedEntry.parentChangesetId
        ? entryByChangesetId(entries, selectedEntry.parentChangesetId)
        : null;
      const parentPatchset = parent ? currentPatchset(parent.changeset) : null;
      if (selectedEntry.parentChangesetId && !parentPatchset?.id) {
        throw new Error("The selected changeset's base has no current patchset.");
      }

      const uploaded = await api.uploadBlob({
        data: utf8ToBase64(fileContent),
        slice: stack.authoringSlice
      });
      if (!uploaded.blobId || !uploaded.contentHash) {
        throw new Error("UploadBlob did not return blob metadata.");
      }

      return api.updateChangeset({
        baseCommitId: selectedEntry.changeset.baseCommitId || stack.baseCommitId,
        baseKind: parentPatchset?.id ? "patchset" : "commit",
        basePatchsetId: parentPatchset?.id || "",
        changesetId: selectedEntry.changesetId,
        expectedCurrentPatchsetId: current?.id || "",
        expectedParentPatchsetId: parentPatchset?.id || "",
        fileEdits: [
          {
            blobId: uploaded.blobId,
            contentHash: uploaded.contentHash,
            mode: 0o100644,
            op: "upsert",
            path: normalizeRepositoryPath(trimmedPath)
          }
        ]
      });
    },
    onError: (error) => onError(getErrorMessage(error)),
    onMutate: () => {
      onError("");
      onMessage("");
    },
    onSuccess: async (patchset) => {
      setFilePath("");
      setFileContent("");
      onMessage(
        patchset.number
          ? `Patchset ${patchset.number} added.`
          : "Patchset added."
      );
      await queryClient.invalidateQueries({ queryKey: ["stack", stackId] });
      await onRefresh();
    }
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    patchsetMutation.mutate();
  };

  return (
    <form
      className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
      onSubmit={submit}
    >
      <h2 className="text-sm font-semibold text-zinc-950">Add patchset</h2>
      <p className="mt-1 text-sm text-slate-600">
        Revise the selected changeset without creating a dependent changeset.
      </p>
      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        Selected changeset
        <input
          className={inputClass}
          disabled
          value={
            selectedEntry
              ? `${entryLabel(selectedEntry)} patchset ${currentPatchsetNumber(selectedEntry.changeset)}`
              : "Choose a changeset"
          }
        />
      </label>
      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        Path
        <input
          className={inputClass}
          onChange={(event) => setFilePath(event.target.value)}
          placeholder="/acme/payment/parser.go"
          value={filePath}
        />
      </label>
      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        Content
        <textarea
          className="min-h-36 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
          onChange={(event) => setFileContent(event.target.value)}
          placeholder="package payment"
          value={fileContent}
        />
      </label>
      <button
        className="mt-5 w-full rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
        disabled={patchsetMutation.isPending || !selectedEntry?.changesetId}
        type="submit"
      >
        {patchsetMutation.isPending ? "Adding..." : "Add patchset"}
      </button>
    </form>
  );
}