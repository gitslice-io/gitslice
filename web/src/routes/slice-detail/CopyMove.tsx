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
  destinationProblem,
  existingEntry,
  findDestinationSlice,
  intoFolder,
  resolveDestination,
  type CopyMoveMode,
  type CopyMoveSource,
} from "../../components/source/slice-editing/copyMove";
import { normalizeRepositoryPath } from "../../components/source/sourceUtils";
import { PathCombobox } from "./PathCombobox";

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
  // copy outside this slice.
  canWriteAccount?(account: string): boolean;
  children: ReactNode;
  commitId: string;
  // onLanded opens the destination once the copy or move has landed.
  onLanded(landed: LandedPathOperation): void;
  slice?: SliceRef;
}

export interface LandedPathOperation {
  changesetId: string;
  commitId: string;
  path: string;
  slice: SliceRef;
}

export function CopyMoveProvider({
  canModify,
  canWriteAccount,
  children,
  commitId,
  onLanded,
  slice,
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
          onLanded={onLanded}
          slice={slice}
          source={active.source}
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

export function CopyMoveDialog({
  canModify,
  canWriteAccount,
  commitId,
  mode,
  onClose,
  onLanded,
  slice,
  source,
}: CopyMoveDialogProps) {
  const api = useApi();
  const [input, setInput] = useState(normalizeRepositoryPath(source.path));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const verb = mode === "copy" ? "Copy" : "Move";
  const kind = source.isDirectory ? "folder" : "file";

  // Suggestions list a folder through this slice when it is inside it, and by
  // repository path elsewhere, which shows the accounts and folders the viewer
  // can read. A folder is inside the slice when a path below it is writable.
  const listFolder = useCallback(
    async (directory: string) => {
      const inSlice =
        slice && canModify(`${directory === "/" ? "" : directory}/_`);
      const page = await api.listDirectory({
        commitId,
        pageSize: 500,
        path: directory,
        ...(inSlice ? { slice } : {}),
      });
      return page.entries ?? [];
    },
    [api, canModify, commitId, slice],
  );

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) {
      return;
    }
    setError("");
    setBusy(true);
    try {
      const outcome = await run();
      if (typeof outcome === "string") {
        setError(outcome);
        return;
      }
      onClose();
      onLanded(outcome);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      setBusy(false);
    }
  }

  // run lands the copy or move and says where, or returns why it can't.
  async function run(): Promise<string | LandedPathOperation> {
    if (!slice) {
      return "This slice hasn't loaded yet.";
    }
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

    // A copy outside this slice lands in the slice that covers the
    // destination, which may belong to another account.
    let target = slice;
    const here = canModify(destination);
    if (!here) {
      if (mode === "move") {
        return "A move can't leave this slice. Copy it there instead, then delete the original.";
      }
      const account = accountOfPath(destination);
      if (canWriteAccount && !canWriteAccount(account)) {
        return `You can't change files in ${account || "that account"}. Pick a path in this slice or in an account you can write to.`;
      }
      const found = await findDestinationSlice(api, destination);
      if (!found?.ref) {
        return `No slice you can edit includes ${destination}. Pick a path inside a slice's included folders.`;
      }
      target = found.ref;
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

    const sourcePath = normalizeRepositoryPath(source.path);
    const landed =
      mode === "move"
        ? await api.movePath({
            slice,
            sourcePath,
            destinationPath: destination,
          })
        : await api.copyPath({
            slice: target,
            sourcePath,
            destinationPath: destination,
            ...(here ? {} : { sourceSlice: slice }),
          });
    return {
      changesetId: landed.changeset?.id ?? "",
      commitId: landed.commitId ?? "",
      path: destination,
      slice: target,
    };
  }

  return (
    <Popup
      description={normalizeRepositoryPath(source.path)}
      onClose={onClose}
      open
      title={`${verb} ${kind}`}
    >
      <form className="space-y-3" onSubmit={submit}>
        <PathCombobox
          label={`${verb} to`}
          listFolder={listFolder}
          onChange={setInput}
          source={source}
          value={input}
        />
        <p className="text-xs text-slate-500 dark:text-zinc-400">
          Type the new path. End it with / (or name an existing folder) to put
          the {kind} inside that folder.
          {mode === "copy"
            ? " A copy can go to another slice or account you can write to, such as /acme/docs/x."
            : ""}{" "}
          It lands right away as its own changeset, without review or checks.
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
    </Popup>
  );
}
