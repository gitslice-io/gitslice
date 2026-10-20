import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";

import type { SliceRef } from "../../api/types";
import { useApi } from "../../api/useApi";
import type { ActionMenuItem } from "../../components/source/ActionMenu";
import { Popup } from "../../components/Popup";
import {
  accountOfPath,
  COPY_ENTRY_LIMIT,
  createChangesetWithEdits,
  destinationProblem,
  existingEntry,
  findDestinationSlice,
  intoFolder,
  planCopy,
  planMove,
  readCopySource,
  resolveDestination,
  skippedEntries,
  type CopyMoveMode,
  type CopyMoveSource,
} from "../../components/source/slice-editing/copyMove";
import type { PendingEdit } from "../../components/source/slice-editing/pendingEdits";
import { normalizeRepositoryPath } from "../../components/source/sourceUtils";

interface CopyMoveContextValue {
  request(mode: CopyMoveMode, source: CopyMoveSource): void;
}

const CopyMoveContext = createContext<CopyMoveContextValue | null>(null);

// useCopyMove is null where the person can't edit the slice, so the menus only
// offer Copy and Move to writers.
export function useCopyMove() {
  return useContext(CopyMoveContext);
}

interface CopyMoveProviderProps {
  canModify(path: string): boolean;
  // Whether the viewer may change files in another account's slices, for a
  // destination outside this slice.
  canWriteAccount?(account: string): boolean;
  children: ReactNode;
  commitId: string;
  // Opens a changeset's page, for the links shown once a copy or move that
  // leaves this slice has been staged.
  openChangeset?(id: string): void;
  slice?: SliceRef;
  // stage puts edits in this slice's draft changeset and opens it.
  stage(edits: PendingEdit[]): void | Promise<void>;
  // stageQuietly does the same without opening it, returning the changeset id.
  stageQuietly?(edits: PendingEdit[]): Promise<string>;
}

export function CopyMoveProvider({
  canModify,
  canWriteAccount,
  children,
  commitId,
  openChangeset,
  slice,
  stage,
  stageQuietly,
}: CopyMoveProviderProps) {
  const [active, setActive] = useState<{
    mode: CopyMoveMode;
    source: CopyMoveSource;
  } | null>(null);
  const request = useCallback(
    (mode: CopyMoveMode, source: CopyMoveSource) => setActive({ mode, source }),
    [],
  );
  const value = useMemo(() => ({ request }), [request]);

  return (
    <CopyMoveContext.Provider value={value}>
      {children}
      {active ? (
        <CopyMoveDialog
          canModify={canModify}
          canWriteAccount={canWriteAccount}
          commitId={commitId}
          mode={active.mode}
          onClose={() => setActive(null)}
          openChangeset={openChangeset}
          slice={slice}
          source={active.source}
          stage={stage}
          stageQuietly={stageQuietly}
        />
      ) : null}
    </CopyMoveContext.Provider>
  );
}

// copyMoveMenuItems are the "Copy to…" and "Move to…" entries of an item's
// menu. Moving needs the source to be modifiable; copying only reads it.
export function copyMoveMenuItems(
  copyMove: CopyMoveContextValue | null,
  source: CopyMoveSource,
  options: { canModifySource: boolean; disabledTitle?: string },
): ActionMenuItem[] {
  if (!copyMove) {
    return [];
  }
  return [
    {
      label: "Copy to…",
      onSelect: () => copyMove.request("copy", source),
    },
    {
      label: "Move to…",
      disabled: !options.canModifySource,
      onSelect: () => copyMove.request("move", source),
      title: options.canModifySource ? undefined : options.disabledTitle,
    },
  ];
}

interface CopyMoveDialogProps extends Omit<CopyMoveProviderProps, "children"> {
  mode: CopyMoveMode;
  onClose(): void;
  source: CopyMoveSource;
}

// CrossSliceResult is what a copy or move that left this slice staged, and
// where: the copy in the destination slice, and for a move the delete of the
// original in this one.
interface CrossSliceResult {
  copyId: string;
  copySlice: string;
  deleteId?: string;
  deleteError?: string;
}

export function CopyMoveDialog({
  canModify,
  canWriteAccount,
  commitId,
  mode,
  onClose,
  openChangeset,
  slice,
  source,
  stage,
  stageQuietly,
}: CopyMoveDialogProps) {
  const api = useApi();
  const [input, setInput] = useState(normalizeRepositoryPath(source.path));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<CrossSliceResult | null>(null);
  const verb = mode === "copy" ? "Copy" : "Move";
  const kind = source.isDirectory ? "folder" : "file";

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) {
      return;
    }
    setError("");
    setBusy(true);
    try {
      const outcome = await run();
      if (typeof outcome === "object") {
        setResult(outcome);
        return;
      }
      if (outcome) {
        setError(outcome);
        return;
      }
      onClose();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      setBusy(false);
    }
  }

  // run returns a message to show, "" once the edits are staged in this slice,
  // or what was staged elsewhere.
  async function run(): Promise<string | CrossSliceResult> {
    const resolved = resolveDestination(input, source);
    if ("error" in resolved) {
      return resolved.error;
    }
    let destination = resolved.into
      ? intoFolder(resolved.path, source)
      : resolved.path;
    // Everything but "is it writable" is checked first, against any slice.
    let problem = destinationProblem({
      canModify: () => true,
      destination,
      mode,
      source,
    });
    if (problem) {
      return problem;
    }

    // A destination outside this slice goes into a changeset of the slice
    // that covers it, which may belong to another account.
    let target = slice;
    let targetLabel = "";
    const here = canModify(destination);
    if (!here) {
      const account = accountOfPath(destination);
      if (canWriteAccount && !canWriteAccount(account)) {
        return `You can't change files in ${account || "that account"}. Pick a path in this slice or in an account you can write to.`;
      }
      const found = await findDestinationSlice(api, destination);
      if (!found?.ref) {
        return `No slice you can edit includes ${destination}. Pick a path inside a slice's included folders.`;
      }
      target = found.ref;
      targetLabel = `${found.ref.account}/${found.ref.slice}`;
    }

    let existing = await existingEntry(api, {
      commitId,
      path: destination,
      slice: target,
    });
    if (existing?.kind === "ENTRY_KIND_DIRECTORY" && !resolved.into) {
      // Naming a folder that exists means "put it in there".
      destination = intoFolder(destination, source);
      problem = destinationProblem({
        canModify: () => true,
        destination,
        mode,
        source,
      });
      if (problem) {
        return problem;
      }
      existing = await existingEntry(api, {
        commitId,
        path: destination,
        slice: target,
      });
    }
    if (existing) {
      return `${destination} already exists. Choose another name.`;
    }

    if (mode === "move" && here) {
      await stage(planMove(source, destination));
      return "";
    }
    const entries = await readCopySource(api, { commitId, slice, source });
    const skipped = skippedEntries(entries);
    if (skipped) {
      return `This ${kind} contains ${skipped} symlink${skipped === 1 ? "" : "s"} or special files, which can't be copied here.`;
    }
    const edits = planCopy(source, destination, entries);
    if (!edits.length || (!source.isDirectory && edits[0].kind !== "write")) {
      return `Couldn't read ${source.path} to copy it.`;
    }
    if (here) {
      await stage(edits);
      return "";
    }

    if (!target || (mode === "move" && !stageQuietly)) {
      return "This page can't stage changes in another slice.";
    }
    const from = slice ? `${slice.account}/${slice.slice}` : "this slice";
    const copyId = await createChangesetWithEdits(api, {
      baseCommitId: commitId,
      description:
        mode === "move"
          ? `Moved from ${normalizeRepositoryPath(source.path)} in ${from}. The original is deleted by a separate changeset in ${from}; land this one first.`
          : `Copied from ${normalizeRepositoryPath(source.path)} in ${from}.`,
      edits,
      slice: target,
      title: `${verb} ${normalizeRepositoryPath(source.path)} to ${destination}`,
    });
    const staged: CrossSliceResult = { copyId, copySlice: targetLabel };
    if (mode === "move") {
      try {
        staged.deleteId = await stageQuietly?.([
          { kind: "delete", path: source.path },
        ]);
      } catch (failure) {
        staged.deleteError =
          failure instanceof Error ? failure.message : String(failure);
      }
    }
    return staged;
  }

  return (
    <Popup
      description={normalizeRepositoryPath(source.path)}
      onClose={onClose}
      open
      title={`${verb} ${kind}`}
    >
      {result ? (
        <CrossSliceSummary
          mode={mode}
          onClose={onClose}
          openChangeset={openChangeset}
          result={result}
        />
      ) : (
        <form className="space-y-3" onSubmit={submit}>
          <label className="grid gap-1 text-sm font-medium text-slate-700 dark:text-zinc-300">
            {verb} to
            <input
              autoFocus
              className="h-10 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 font-mono text-sm text-zinc-950 dark:text-zinc-50 outline-none transition placeholder:text-slate-400 dark:placeholder:text-zinc-500 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700"
              onChange={(event) => setInput(event.target.value)}
              spellCheck={false}
              value={input}
            />
          </label>
          <p className="text-xs text-slate-500 dark:text-zinc-400">
            Type the new path. End it with / (or name an existing folder) to put
            the {kind} inside that folder.
            {mode === "copy" && source.isDirectory
              ? ` Copies up to ${COPY_ENTRY_LIMIT} items.`
              : ""}{" "}
            A path in another account or slice (such as /acme/docs/x) goes
            into a changeset of that slice. It is staged in a draft changeset
            for review.
          </p>
          {error ? (
            <p
              className="rounded-md border border-red-200 dark:border-red-900 bg-red-50 dark:bg-red-950/40 px-3 py-2 text-sm text-red-700 dark:text-red-300"
              role="alert"
            >
              {error}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <button
              className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
              onClick={onClose}
              type="button"
            >
              Cancel
            </button>
            <button
              className="rounded-md bg-zinc-950 dark:bg-zinc-100 px-3 py-2 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-60"
              disabled={busy}
              type="submit"
            >
              {busy ? (mode === "copy" ? "Copying…" : "Moving…") : verb}
            </button>
          </div>
        </form>
      )}
    </Popup>
  );
}

function CrossSliceSummary({
  mode,
  onClose,
  openChangeset,
  result,
}: {
  mode: CopyMoveMode;
  onClose(): void;
  openChangeset?(id: string): void;
  result: CrossSliceResult;
}) {
  const link = (id: string, label: string) => (
    <button
      className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
      onClick={() => {
        onClose();
        openChangeset?.(id);
      }}
      type="button"
    >
      {label}
    </button>
  );
  return (
    <div className="space-y-3 text-sm text-slate-700 dark:text-zinc-300">
      <p>
        The copy is staged in a draft changeset in{" "}
        <span className="font-mono">{result.copySlice}</span>.
      </p>
      {mode === "move" ? (
        result.deleteId ? (
          <p>
            Deleting the original is staged in a separate draft changeset in
            this slice. A changeset edits one slice, so land the copy first,
            then the delete.
          </p>
        ) : (
          <p
            className="rounded-md border border-red-200 dark:border-red-900 bg-red-50 dark:bg-red-950/40 px-3 py-2 text-red-700 dark:text-red-300"
            role="alert"
          >
            The original wasn't deleted: {result.deleteError}. Delete it from
            its menu once the copy has landed.
          </p>
        )
      ) : null}
      <div className="flex flex-wrap justify-end gap-2">
        {link(result.copyId, "Open the copy")}
        {result.deleteId ? link(result.deleteId, "Open the delete") : null}
        <button
          className="rounded-md bg-zinc-950 dark:bg-zinc-100 px-3 py-2 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98]"
          onClick={onClose}
          type="button"
        >
          Done
        </button>
      </div>
    </div>
  );
}
