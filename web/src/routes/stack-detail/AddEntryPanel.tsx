import type { ChangesetStackEntry } from "../../api/types";
import { useState, useEffect, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import {
  entryByChangesetId,
  entryLabel,
  currentPatchset,
  getErrorMessage
} from "../stackPageUtils";
import { inputClass } from "./stackUtils";

export function AddEntryPanel({
  entries,
  onError,
  onMessage,
  onRefresh,
  onSelect,
  selectedEntry,
  stackId
}: {
  entries: ChangesetStackEntry[];
  onError(message: string): void;
  onMessage(message: string): void;
  onRefresh(): Promise<void>;
  onSelect(entryId: string): void;
  selectedEntry: ChangesetStackEntry | null;
  stackId: string;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [mode, setMode] = useState<"child" | "sibling">("child");
  const [parentId, setParentId] = useState(selectedEntry?.changesetId || "");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");

  useEffect(() => {
    if (!selectedEntry?.changesetId) {
      return;
    }
    if (mode === "child") {
      setParentId(selectedEntry.changesetId);
    } else {
      setParentId(selectedEntry.parentChangesetId || "");
    }
  }, [mode, selectedEntry?.changesetId, selectedEntry?.parentChangesetId]);

  const addMutation = useMutation({
    mutationFn: async () => {
      if (!stackId) {
        throw new Error("This dependency tree did not return an id.");
      }
      const trimmedTitle = title.trim();
      if (!trimmedTitle) {
        throw new Error("Enter a changeset title.");
      }

      if (mode === "sibling" && selectedEntry && !selectedEntry.parentChangesetId) {
        throw new Error("The root changeset cannot have a sibling.");
      }

      const effectiveParentId =
        mode === "sibling" && selectedEntry
          ? selectedEntry.parentChangesetId || ""
          : parentId;
      const parent = effectiveParentId ? entryByChangesetId(entries, effectiveParentId) : null;
      const parentPatchset = parent ? currentPatchset(parent.changeset)?.id : "";
      if (parent && !parentPatchset) {
        throw new Error("The selected base changeset has no current patchset.");
      }

      return api.addStackEntry({
        description: description.trim(),
        parentChangesetId: parent?.changesetId || "",
        parentPatchsetId: parentPatchset,
        stackId,
        title: trimmedTitle
      });
    },
    onError: (error) => onError(getErrorMessage(error)),
    onMutate: () => {
      onError("");
      onMessage("");
    },
    onSuccess: async (changeset) => {
      setTitle("");
      setDescription("");
      onMessage("Changeset created.");
      await queryClient.invalidateQueries({ queryKey: ["stack", stackId] });
      await onRefresh();
      if (changeset.id) {
        onSelect(changeset.id);
      }
    }
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    addMutation.mutate();
  };

  return (
    <form
      className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
      onSubmit={submit}
    >
      <h2 className="text-sm font-semibold text-zinc-950">Add changeset</h2>
      <div className="mt-4 grid grid-cols-2 rounded-md border border-slate-300 bg-slate-50 p-1">
        {(["child", "sibling"] as const).map((option) => (
          <button
            className={cn(
              "rounded px-3 py-1.5 text-sm font-medium transition",
              mode === option
                ? "bg-white text-zinc-950 shadow-sm"
                : "text-slate-600 hover:text-zinc-950"
            )}
            key={option}
            onClick={() => setMode(option)}
            type="button"
          >
            {option === "child" ? "Dependent" : "Sibling"}
          </button>
        ))}
      </div>
      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        Base changeset
        <select
          className={inputClass}
          disabled={!entries.length || mode === "sibling"}
          onChange={(event) => setParentId(event.target.value)}
          value={parentId}
        >
          <option disabled={entries.length > 0 && mode === "child"} value="">
            Root changeset
          </option>
          {entries.map((entry) => (
            <option key={entry.changesetId} value={entry.changesetId}>
              {entryLabel(entry)}
            </option>
          ))}
        </select>
      </label>
      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        Title
        <input
          className={inputClass}
          onChange={(event) => setTitle(event.target.value)}
          placeholder="Use parser in API"
          value={title}
        />
      </label>
      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        Description
        <textarea
          className="min-h-24 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
          onChange={(event) => setDescription(event.target.value)}
          placeholder="Optional review context"
          value={description}
        />
      </label>
      <button
        className="mt-5 w-full rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
        disabled={addMutation.isPending}
        type="submit"
      >
        {addMutation.isPending ? "Adding..." : mode === "child" ? "Add dependent" : "Add sibling"}
      </button>
    </form>
  );
}