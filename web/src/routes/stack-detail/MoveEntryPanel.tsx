import type { ChangesetStackEntry } from "../../api/types";
import { useState, useEffect, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useApi } from "../../api/useApi";
import {
  entryByChangesetId,
  entryLabel,
  currentPatchset,
  getErrorMessage
} from "../stackPageUtils";
import { inputClass } from "./stackUtils";

export function MoveEntryPanel({
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
  const [changesetId, setChangesetId] = useState(selectedEntry?.changesetId || "");
  const [parentId, setParentId] = useState("");
  const [siblingOrder, setSiblingOrder] = useState("");

  useEffect(() => {
    if (selectedEntry?.changesetId) {
      setChangesetId(selectedEntry.changesetId);
    }
  }, [selectedEntry?.changesetId]);

  const moveMutation = useMutation({
    mutationFn: async () => {
      if (!stackId) {
        throw new Error("This dependency tree did not return an id.");
      }
      if (!changesetId) {
        throw new Error("Choose a changeset to move.");
      }
      const parent = parentId ? entryByChangesetId(entries, parentId) : null;
      if (parent?.changesetId === changesetId) {
        throw new Error("A changeset cannot be based on itself.");
      }
      const parentPatchsetId = parent ? currentPatchset(parent.changeset)?.id : "";
      if (parent && !parentPatchsetId) {
        throw new Error("The new base has no current patchset.");
      }

      await api.reparentStackEntry({
        changesetId,
        newParentChangesetId: parent?.changesetId || "",
        newParentPatchsetId: parentPatchsetId,
        siblingOrder: siblingOrder.trim(),
        stackId
      });

      return api.restack({
        stackId,
        startChangesetId: changesetId
      });
    },
    onError: (error) => onError(getErrorMessage(error)),
    onMutate: () => {
      onError("");
      onMessage("");
    },
    onSuccess: async (result) => {
      const count = result.entries?.length ?? 0;
      onMessage(
        count
          ? `Changeset moved and ${count} dependents updated.`
          : "Changeset moved. No dependent patchsets needed updates."
      );
      await queryClient.invalidateQueries({ queryKey: ["stack", stackId] });
      await onRefresh();
      onSelect(changesetId);
    }
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    moveMutation.mutate();
  };

  return (
    <form
      className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
      onSubmit={submit}
    >
      <h2 className="text-sm font-semibold text-zinc-950">Move to base</h2>
      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        Changeset
        <select
          className={inputClass}
          onChange={(event) => setChangesetId(event.target.value)}
          value={changesetId}
        >
          {entries.map((entry) => (
            <option key={entry.changesetId} value={entry.changesetId}>
              {entryLabel(entry)}
            </option>
          ))}
        </select>
      </label>
      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        New base
        <select
          className={inputClass}
          onChange={(event) => setParentId(event.target.value)}
          value={parentId}
        >
          <option value="">Root</option>
          {entries
            .filter((entry) => entry.changesetId !== changesetId)
            .map((entry) => (
              <option key={entry.changesetId} value={entry.changesetId}>
                {entryLabel(entry)}
              </option>
            ))}
        </select>
      </label>
      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        Sibling order
        <input
          className={inputClass}
          inputMode="numeric"
          onChange={(event) => setSiblingOrder(event.target.value)}
          placeholder="Append"
          value={siblingOrder}
        />
      </label>
      <button
        className="mt-5 w-full rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
        disabled={moveMutation.isPending || entries.length === 0}
        type="submit"
      >
        {moveMutation.isPending ? "Moving..." : "Move and update"}
      </button>
    </form>
  );
}