import { Link, useNavigate } from "@tanstack/react-router";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent
} from "react";

import type { Changeset, FileEdit, SliceRef } from "../../api/types";
import { type ApiClient } from "../../api/useApi";
import { normalizeRepositoryPath } from "./sourceUtils";

export type PendingEdit =
  | {
      kind: "write";
      path: string;
      content?: string;
      isNew: boolean;
      blobId?: string;
      contentHash?: string;
    }
  | { kind: "mkdir"; path: string }
  | { kind: "rename"; oldPath: string; path: string }
  | { kind: "delete"; path: string };

type DraftSaveStatus = "idle" | "adopting" | "saving" | "saved" | "failed";
type DraftActionStatus = "idle" | "submitting" | "discarding";

interface DraftChangesetControllerOptions {
  api: ApiClient;
  commitId: string;
  sliceLabel: string;
  sliceRef: SliceRef | undefined;
  subjectId: string;
}

export interface DraftChangesetController {
  actionStatus: DraftActionStatus;
  changesetHandle: string;
  changesetId: string;
  currentPatchsetId: string;
  edits: PendingEdit[];
  errorMessage: string;
  removeEdit(key: string): void;
  retrySave(): void;
  saveStatus: DraftSaveStatus;
  stageEdit(edit: PendingEdit): void;
  discardDraft(): Promise<void>;
  submitDraft(): Promise<string>;
}

interface DirectoryCreateControlsProps {
  directoryPath: string;
  onStageEdit(edit: PendingEdit): void;
}

interface InlineRenameFormProps {
  directoryPath?: string;
  onCancel(): void;
  onSave(name: string): void;
  originalName: string;
}

export function useDraftChangesetController({
  api,
  commitId,
  sliceLabel,
  sliceRef,
  subjectId
}: DraftChangesetControllerOptions): DraftChangesetController {
  const defaultTitle = `Web edits to ${sliceLabel}`;
  const [changesetId, setChangesetId] = useState("");
  const [changesetHandle, setChangesetHandle] = useState("");
  const [currentPatchsetId, setCurrentPatchsetId] = useState("");
  const [edits, setEdits] = useState<PendingEdit[]>([]);
  const [saveStatus, setSaveStatus] = useState<DraftSaveStatus>("idle");
  const [actionStatus, setActionStatus] =
    useState<DraftActionStatus>("idle");
  const [errorMessage, setErrorMessage] = useState("");

  const changesetIdRef = useRef("");
  const changesetHandleRef = useRef("");
  const currentPatchsetIdRef = useRef("");
  const editsRef = useRef<PendingEdit[]>([]);
  const errorMessageRef = useRef("");
  const flushTailRef = useRef<Promise<void>>(Promise.resolve());
  const generationRef = useRef(0);

  const setControllerError = useCallback((message: string) => {
    errorMessageRef.current = message;
    setErrorMessage(message);
  }, []);

  const clearControllerError = useCallback(() => {
    errorMessageRef.current = "";
    setErrorMessage("");
  }, []);

  const setDraftIdentity = useCallback(
    (changeset: Changeset | undefined) => {
      const nextChangesetId = changeset?.id ?? "";
      const nextHandle = changeset?.handle ?? "";
      const nextPatchsetId = changeset?.currentPatchsetId ?? "";

      changesetIdRef.current = nextChangesetId;
      changesetHandleRef.current = nextHandle;
      currentPatchsetIdRef.current = nextPatchsetId;
      setChangesetId(nextChangesetId);
      setChangesetHandle(nextHandle);
      setCurrentPatchsetId(nextPatchsetId);
    },
    []
  );

  useEffect(() => {
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    changesetIdRef.current = "";
    changesetHandleRef.current = "";
    currentPatchsetIdRef.current = "";
    editsRef.current = [];
    setChangesetId("");
    setChangesetHandle("");
    setCurrentPatchsetId("");
    setEdits([]);
    clearControllerError();

    if (!commitId || !sliceRef?.account || !sliceRef?.slice || !subjectId) {
      setSaveStatus("idle");
      return;
    }

    let cancelled = false;
    setSaveStatus("adopting");

    async function adoptDraft() {
      try {
        const listed = await api.listChangesets({
          authoringSlice: sliceRef,
          status: "draft",
          limit: 50
        });
        const draft = (listed.changesets ?? []).find(
          (changeset) => changeset.author === subjectId
        );

        if (cancelled || generationRef.current !== generation) {
          return;
        }

        if (!draft?.id && !draft?.handle) {
          setSaveStatus("idle");
          return;
        }

        const fullDraft = await api.getChangeset({
          changesetId: draft.id || draft.handle
        });

        if (cancelled || generationRef.current !== generation) {
          return;
        }

        const currentPatchset = currentChangesetPatchset(fullDraft);
        const hydratedEdits = (currentPatchset?.fileEdits ?? [])
          .map(fileEditToPendingEdit)
          .filter((edit): edit is PendingEdit => Boolean(edit));

        setDraftIdentity(fullDraft);
        currentPatchsetIdRef.current =
          currentPatchset?.id ?? fullDraft.currentPatchsetId ?? "";
        setCurrentPatchsetId(currentPatchsetIdRef.current);
        editsRef.current = hydratedEdits;
        setEdits(hydratedEdits);
        setSaveStatus("saved");
      } catch (error) {
        if (cancelled || generationRef.current !== generation) {
          return;
        }
        setSaveStatus("failed");
        setControllerError(errorMessageFromUnknown(error));
      }
    }

    void adoptDraft();

    return () => {
      cancelled = true;
    };
  }, [api, clearControllerError, commitId, setControllerError, setDraftIdentity, sliceRef, subjectId]);

  const clearDraftState = useCallback(() => {
    changesetIdRef.current = "";
    changesetHandleRef.current = "";
    currentPatchsetIdRef.current = "";
    editsRef.current = [];
    setChangesetId("");
    setChangesetHandle("");
    setCurrentPatchsetId("");
    setEdits([]);
    clearControllerError();
    setSaveStatus("idle");
  }, [clearControllerError]);

  const persistEdits = useCallback(
    async (nextEdits: PendingEdit[], generation: number) => {
      if (!commitId) {
        throw new Error("Latest global state did not return a commit id.");
      }
      if (!sliceRef?.account || !sliceRef?.slice) {
        throw new Error("Authoring slice is missing account or slice.");
      }

      clearControllerError();
      setSaveStatus("saving");

      let draftId = changesetIdRef.current;
      let draftPatchsetId = currentPatchsetIdRef.current;

      if (!draftId) {
        if (!nextEdits.length) {
          setSaveStatus("idle");
          return;
        }

        const changeset = await api.createChangeset({
          authoringSlice: sliceRef,
          baseCommitId: commitId,
          title: defaultTitle,
          description: ""
        });

        if (!changeset.id) {
          throw new Error("CreateChangeset did not return a changeset id.");
        }

        if (generationRef.current !== generation) {
          return;
        }

        draftId = changeset.id;
        draftPatchsetId = changeset.currentPatchsetId ?? "";
        changesetIdRef.current = draftId;
        changesetHandleRef.current = changeset.handle ?? "";
        currentPatchsetIdRef.current = draftPatchsetId;
        setChangesetId(draftId);
        setChangesetHandle(changeset.handle ?? "");
        setCurrentPatchsetId(draftPatchsetId);
      }

      const fileEdits = await preparePendingFileEdits(api, nextEdits, sliceRef);
      const patchset = await api.updateChangeset({
        changesetId: draftId,
        expectedCurrentPatchsetId: draftPatchsetId,
        baseCommitId: commitId,
        fileEdits
      });

      if (generationRef.current !== generation) {
        return;
      }

      if (!patchset.id) {
        throw new Error("UpdateChangeset did not return a patchset id.");
      }

      currentPatchsetIdRef.current = patchset.id ?? "";
      setCurrentPatchsetId(patchset.id ?? "");
      setSaveStatus("saved");
    },
    [api, clearControllerError, commitId, defaultTitle, sliceRef]
  );

  const queueFlush = useCallback(
    (nextEdits: PendingEdit[]) => {
      const generation = generationRef.current;
      const run = flushTailRef.current.then(
        () => persistEdits(nextEdits, generation),
        () => persistEdits(nextEdits, generation)
      );

      flushTailRef.current = run.catch(() => undefined);
      void run.catch((error) => {
        if (generationRef.current !== generation) {
          return;
        }
        setSaveStatus("failed");
        setControllerError(errorMessageFromUnknown(error));
      });
    },
    [persistEdits, setControllerError]
  );

  const applyEdits = useCallback(
    (nextEdits: PendingEdit[]) => {
      editsRef.current = nextEdits;
      setEdits(nextEdits);
      queueFlush(nextEdits);
    },
    [queueFlush]
  );

  const stageEdit = useCallback(
    (edit: PendingEdit) => {
      applyEdits(upsertPendingEdit(editsRef.current, edit));
    },
    [applyEdits]
  );

  const removeEdit = useCallback(
    (key: string) => {
      applyEdits(removePendingEdit(editsRef.current, key));
    },
    [applyEdits]
  );

  const retrySave = useCallback(() => {
    queueFlush(editsRef.current);
  }, [queueFlush]);

  const submitDraft = useCallback(async () => {
    setActionStatus("submitting");
    try {
      await flushTailRef.current;
      if (errorMessageRef.current) {
        throw new Error("Save failed. Retry the draft before submitting.");
      }

      const id = changesetIdRef.current;
      const expectedCurrentPatchsetId = currentPatchsetIdRef.current;
      if (!id) {
        throw new Error("There is no draft changeset to submit.");
      }
      if (!expectedCurrentPatchsetId) {
        throw new Error("The draft changeset has no patchset to submit.");
      }

      await api.submitChangeset({
        changesetId: id,
        expectedCurrentPatchsetId
      });

      const navigateId = changesetHandleRef.current || id;
      clearDraftState();
      return navigateId;
    } finally {
      setActionStatus("idle");
    }
  }, [api, clearDraftState]);

  const discardDraft = useCallback(async () => {
    setActionStatus("discarding");
    try {
      await flushTailRef.current;
      const id = changesetIdRef.current;
      if (id) {
        await api.abandonChangeset({
          changesetId: id,
          reason: "discarded from web"
        });
      }
      clearDraftState();
    } finally {
      setActionStatus("idle");
    }
  }, [api, clearDraftState]);

  return {
    actionStatus,
    changesetHandle,
    changesetId,
    currentPatchsetId,
    edits,
    errorMessage,
    removeEdit,
    retrySave,
    saveStatus,
    stageEdit,
    discardDraft,
    submitDraft
  };
}

export function DraftChangesetPanel({
  actionStatus,
  changesetHandle,
  changesetId,
  currentPatchsetId,
  edits,
  errorMessage,
  removeEdit,
  retrySave,
  saveStatus,
  discardDraft,
  submitDraft
}: DraftChangesetController) {
  const navigate = useNavigate();
  const [actionError, setActionError] = useState("");
  const detailId = changesetHandle || changesetId;
  const isAdopting = saveStatus === "adopting";
  const isSaving = saveStatus === "saving";
  const isActionPending = actionStatus !== "idle";
  const hasDraft = Boolean(changesetId || edits.length);

  if (!hasDraft && !isAdopting && !errorMessage) {
    return null;
  }

  async function submit() {
    setActionError("");
    try {
      const id = await submitDraft();
      void navigate({
        to: "/changesets/$id",
        params: { id }
      });
    } catch (error) {
      setActionError(errorMessageFromUnknown(error));
    }
  }

  async function discard() {
    setActionError("");
    try {
      await discardDraft();
    } catch (error) {
      setActionError(errorMessageFromUnknown(error));
    }
  }

  return (
    <section className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <h2 className="text-base font-semibold text-zinc-950">
            Draft changeset
          </h2>
          <div className="mt-1 flex min-w-0 flex-wrap items-center gap-2 text-sm text-slate-600">
            {detailId ? (
              <Link
                className="break-all font-mono text-zinc-900 underline decoration-slate-300 underline-offset-4 hover:decoration-slate-700"
                params={{ id: detailId }}
                to="/changesets/$id"
              >
                {detailId}
              </Link>
            ) : (
              <span>Draft will be created with the first saved edit.</span>
            )}
            <span className="inline-flex rounded-md border border-slate-200 bg-slate-50 px-2 py-1 text-xs font-semibold text-slate-700">
              {draftStatusLabel(saveStatus)}
            </span>
          </div>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          <button
            className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-60"
            disabled={isActionPending || isAdopting || isSaving || !hasDraft}
            onClick={() => void discard()}
            type="button"
          >
            {actionStatus === "discarding" ? "Discarding..." : "Discard"}
          </button>
          <button
            className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-60"
            disabled={
              isActionPending ||
              isAdopting ||
              isSaving ||
              !changesetId ||
              !currentPatchsetId ||
              !edits.length
            }
            onClick={() => void submit()}
            type="button"
          >
            {actionStatus === "submitting" ? "Submitting..." : "Submit"}
          </button>
        </div>
      </div>

      {edits.length ? (
        <ul className="mt-4 divide-y divide-slate-100 rounded-lg border border-slate-200">
          {edits.map((edit) => {
            const key = pendingEditKey(edit);
            return (
              <li
                className="flex min-w-0 flex-col gap-3 p-3 sm:flex-row sm:items-center sm:justify-between"
                key={key}
              >
                <div className="min-w-0">
                  <span className="inline-flex rounded-md border border-slate-200 bg-slate-50 px-2 py-1 text-xs font-semibold text-slate-700">
                    {pendingEditLabel(edit)}
                  </span>
                  <div className="mt-2 break-all font-mono text-sm text-zinc-950">
                    {edit.kind === "rename" ? (
                      <>
                        {edit.oldPath} <span className="text-slate-400">to</span>{" "}
                        {edit.path}
                      </>
                    ) : (
                      edit.path
                    )}
                  </div>
                </div>
                <button
                  className="w-fit rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-60"
                  disabled={isActionPending || isAdopting}
                  onClick={() => removeEdit(key)}
                  type="button"
                >
                  Remove
                </button>
              </li>
            );
          })}
        </ul>
      ) : (
        <p className="mt-4 rounded-lg border border-dashed border-slate-300 p-3 text-sm text-slate-600">
          No file edits in this draft.
        </p>
      )}

      {errorMessage ? (
        <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-900">
          {errorMessage}
          <button
            className="ml-3 font-semibold underline decoration-rose-300 underline-offset-4 hover:decoration-rose-800 disabled:cursor-not-allowed disabled:opacity-60"
            disabled={isActionPending || isAdopting || isSaving}
            onClick={retrySave}
            type="button"
          >
            Retry
          </button>
        </p>
      ) : null}
      {actionError ? (
        <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-900">
          {actionError}
        </p>
      ) : null}
    </section>
  );
}

export function DirectoryCreateControls({
  directoryPath,
  onStageEdit
}: DirectoryCreateControlsProps) {
  const [mode, setMode] = useState<"file" | "folder" | "">("");

  // Without a concrete current folder we'd build account-only paths the server
  // rejects, so require navigating into a folder first.
  if (!directoryPath) {
    return (
      <div className="border-b border-slate-200 bg-slate-50 px-5 py-3 text-sm text-slate-600">
        Open a folder to add files or folders here.
      </div>
    );
  }

  return (
    <div className="border-b border-slate-200 bg-slate-50 px-5 py-4">
      <p className="mb-2 min-w-0 break-all text-xs text-slate-500">
        New items are created in{" "}
        <span className="font-mono text-slate-700">{directoryPath}</span>
      </p>
      <div className="flex flex-wrap gap-2">
        <button
          className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
          onClick={() => setMode("file")}
          type="button"
        >
          New file
        </button>
        <button
          className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
          onClick={() => setMode("folder")}
          type="button"
        >
          New folder
        </button>
      </div>

      {mode === "file" ? (
        <NewFileForm
          directoryPath={directoryPath}
          onCancel={() => setMode("")}
          onCreate={(edit) => {
            onStageEdit(edit);
            setMode("");
          }}
        />
      ) : null}

      {mode === "folder" ? (
        <NewFolderForm
          directoryPath={directoryPath}
          onCancel={() => setMode("")}
          onCreate={(edit) => {
            onStageEdit(edit);
            setMode("");
          }}
        />
      ) : null}
    </div>
  );
}

export function InlineRenameForm({
  directoryPath,
  onCancel,
  onSave,
  originalName
}: InlineRenameFormProps) {
  const [name, setName] = useState(originalName);
  const nameError = validateEntryName(name);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (nameError) {
      return;
    }
    onSave(name.trim());
  }

  return (
    <form className="grid gap-2" onSubmit={submit}>
      <div className="flex min-w-0 flex-wrap gap-2">
        <input
          className="h-9 min-w-0 flex-1 rounded-md border border-slate-300 bg-white px-2.5 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
          onChange={(event) => setName(event.target.value)}
          value={name}
        />
        <button
          className="rounded-md bg-zinc-950 px-2.5 py-1.5 text-xs font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-60"
          disabled={Boolean(nameError)}
          type="submit"
        >
          Save
        </button>
        <button
          className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
          onClick={onCancel}
          type="button"
        >
          Cancel
        </button>
      </div>
      {directoryPath !== undefined ? (
        <EntryPathPreview
          directoryPath={directoryPath}
          name={name}
          verb="Renames to"
        />
      ) : nameError && name.trim() ? (
        <p className="text-xs text-rose-700">{nameError}</p>
      ) : null}
    </form>
  );
}

export function upsertPendingEdit(edits: PendingEdit[], edit: PendingEdit) {
  const normalized = normalizePendingEdit(edit);
  const normalizedKey = pendingEditKey(normalized);

  return [
    ...edits.filter(
      (candidate) =>
        pendingEditKey(candidate) !== normalizedKey &&
        !hasConflictingPendingPath(candidate, normalized)
    ),
    normalized
  ];
}

export function pendingEditKey(edit: PendingEdit) {
  if (edit.kind === "rename") {
    return `rename:${normalizeRepositoryPath(edit.oldPath)}`;
  }
  return `${edit.kind}:${normalizeRepositoryPath(edit.path)}`;
}

export function removePendingEdit(edits: PendingEdit[], key: string) {
  return edits.filter((edit) => pendingEditKey(edit) !== key);
}

export function pendingWriteForPath(edits: PendingEdit[], path: string) {
  const normalizedPath = normalizeRepositoryPath(path);
  return edits.find(
    (edit) => edit.kind === "write" && edit.path === normalizedPath
  ) as Extract<PendingEdit, { kind: "write" }> | undefined;
}

export function pendingDeleteForPath(edits: PendingEdit[], path: string) {
  const normalizedPath = normalizeRepositoryPath(path);
  return edits.find(
    (edit) => edit.kind === "delete" && edit.path === normalizedPath
  ) as Extract<PendingEdit, { kind: "delete" }> | undefined;
}

export function pendingRenameForPath(edits: PendingEdit[], path: string) {
  const normalizedPath = normalizeRepositoryPath(path);
  return edits.find(
    (edit) => edit.kind === "rename" && edit.oldPath === normalizedPath
  ) as Extract<PendingEdit, { kind: "rename" }> | undefined;
}

export function pendingChildrenForDirectory(edits: PendingEdit[], directoryPath: string) {
  const normalizedDirectory = normalizeDirectoryPath(directoryPath);
  return edits.filter((edit) => {
    if (edit.kind === "write" && edit.isNew) {
      return parentRepositoryPath(edit.path) === normalizedDirectory;
    }
    if (edit.kind === "mkdir") {
      return parentRepositoryPath(edit.path) === normalizedDirectory;
    }
    return false;
  }) as Array<
    Extract<PendingEdit, { kind: "write" }> | Extract<PendingEdit, { kind: "mkdir" }>
  >;
}

export function joinRepositoryPath(directoryPath: string, name: string) {
  const cleanName = name.trim();
  const normalizedDirectory = normalizeDirectoryPath(directoryPath);
  return normalizeRepositoryPath(
    normalizedDirectory ? `${normalizedDirectory}/${cleanName}` : cleanName
  );
}

export function parentRepositoryPath(path: string) {
  const parts = normalizeRepositoryPath(path).split("/").filter(Boolean);
  if (parts.length <= 1) {
    return "";
  }
  return `/${parts.slice(0, -1).join("/")}`;
}

export function repositoryPathName(path: string) {
  const parts = normalizeRepositoryPath(path).split("/").filter(Boolean);
  return parts[parts.length - 1] ?? "";
}

// Validates a name or relative path (nested paths like "docs/notes.md" are
// allowed). Each "/"-separated segment must be a valid path segment so the edit
// the server receives is canonical: no empty segments, no "." / "..", no
// surrounding whitespace, and no control characters.
export function validateEntryName(name: string) {
  const normalized = name.trim().replace(/\\/g, "/");
  if (!normalized) {
    return "Enter a name.";
  }
  const segments = normalized.split("/");
  for (const segment of segments) {
    if (segment === "") {
      return "Path can't have empty segments (no leading, trailing, or double slashes).";
    }
    if (segment !== segment.trim()) {
      return "Path segments can't start or end with a space.";
    }
    if (segment === "." || segment === "..") {
      return 'Path segments can’t be "." or "..".';
    }
    // eslint-disable-next-line no-control-regex
    if (/[\u0000-\u001f\u007f]/.test(segment)) {
      return "Path can't contain control characters.";
    }
  }
  return "";
}

// Shows where a new/renamed entry will land as the user types, or the inline
// validation error. Empty input renders nothing.
function EntryPathPreview({
  directoryPath,
  name,
  verb
}: {
  directoryPath: string;
  name: string;
  verb: string;
}) {
  const trimmed = name.trim();
  if (!trimmed) {
    return null;
  }
  const error = validateEntryName(trimmed);
  if (error) {
    return <p className="text-xs text-rose-700">{error}</p>;
  }
  return (
    <p className="break-all text-xs text-slate-500">
      {verb}{" "}
      <span className="font-mono text-slate-700">
        {joinRepositoryPath(directoryPath, trimmed)}
      </span>
    </p>
  );
}

function NewFileForm({
  directoryPath,
  onCancel,
  onCreate
}: {
  directoryPath: string;
  onCancel(): void;
  onCreate(edit: PendingEdit): void;
}) {
  const [name, setName] = useState("");
  const [content, setContent] = useState("");
  const nameError = validateEntryName(name);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (nameError) {
      return;
    }
    onCreate({
      kind: "write",
      path: joinRepositoryPath(directoryPath, name),
      content,
      isNew: true
    });
  }

  return (
    <form className="mt-4 grid gap-3" onSubmit={submit}>
      <label className="grid gap-2 text-sm font-medium text-zinc-800">
        File name
        <input
          className="h-10 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
          onChange={(event) => setName(event.target.value)}
          placeholder="notes.md or docs/notes.md"
          value={name}
        />
      </label>
      <EntryPathPreview
        directoryPath={directoryPath}
        name={name}
        verb="Creates"
      />
      <label className="grid gap-2 text-sm font-medium text-zinc-800">
        Content
        <textarea
          className="min-h-40 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm leading-6 text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
          onChange={(event) => setContent(event.target.value)}
          value={content}
        />
      </label>
      <div className="flex flex-wrap gap-2">
        <button
          className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-60"
          disabled={Boolean(nameError)}
          type="submit"
        >
          Save
        </button>
        <button
          className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
          onClick={onCancel}
          type="button"
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

function NewFolderForm({
  directoryPath,
  onCancel,
  onCreate
}: {
  directoryPath: string;
  onCancel(): void;
  onCreate(edit: PendingEdit): void;
}) {
  const [name, setName] = useState("");
  const nameError = validateEntryName(name);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (nameError) {
      return;
    }
    onCreate({
      kind: "mkdir",
      path: joinRepositoryPath(directoryPath, name)
    });
  }

  return (
    <form className="mt-4 grid gap-3 sm:max-w-md" onSubmit={submit}>
      <label className="grid gap-2 text-sm font-medium text-zinc-800">
        Folder name
        <input
          className="h-10 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
          onChange={(event) => setName(event.target.value)}
          placeholder="docs or docs/images"
          value={name}
        />
      </label>
      <EntryPathPreview
        directoryPath={directoryPath}
        name={name}
        verb="Creates"
      />
      <div className="flex flex-wrap gap-2">
        <button
          className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-60"
          disabled={Boolean(nameError)}
          type="submit"
        >
          Save
        </button>
        <button
          className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
          onClick={onCancel}
          type="button"
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

async function preparePendingFileEdits(
  api: ApiClient,
  edits: PendingEdit[],
  slice: SliceRef
): Promise<FileEdit[]> {
  const uploadedWrites = new Map<string, FileEdit>();
  for (const edit of edits) {
    if (edit.kind !== "write") {
      continue;
    }

    if (edit.content === undefined) {
      if (!edit.blobId || !edit.contentHash) {
        throw new Error(`Draft write for ${edit.path} is missing blob metadata.`);
      }
      uploadedWrites.set(pendingEditKey(edit), {
        op: edit.isNew ? "add" : "update",
        path: edit.path,
        blobId: edit.blobId,
        contentHash: edit.contentHash
      });
      continue;
    }

    const uploaded = await api.uploadBlob({
      data: utf8ToBase64(edit.content),
      slice
    });
    if (!uploaded.blobId || !uploaded.contentHash) {
      throw new Error(`Upload did not return blob metadata for ${edit.path}.`);
    }
    uploadedWrites.set(pendingEditKey(edit), {
      op: edit.isNew ? "add" : "update",
      path: edit.path,
      blobId: uploaded.blobId,
      contentHash: uploaded.contentHash
    });
  }

  const ordered = [...edits].sort(compareEditOrder);
  return ordered.map((edit) => {
    if (edit.kind === "write") {
      const fileEdit = uploadedWrites.get(pendingEditKey(edit));
      if (!fileEdit) {
        throw new Error(`Missing uploaded blob for ${edit.path}.`);
      }
      return fileEdit;
    }
    if (edit.kind === "mkdir") {
      return { op: "mkdir", path: edit.path };
    }
    if (edit.kind === "rename") {
      return { op: "rename", oldPath: edit.oldPath, path: edit.path };
    }
    return { op: "delete", path: edit.path };
  });
}

function compareEditOrder(left: PendingEdit, right: PendingEdit) {
  return editOrder(left.kind) - editOrder(right.kind);
}

function editOrder(kind: PendingEdit["kind"]) {
  switch (kind) {
    case "mkdir":
      return 0;
    case "write":
      return 1;
    case "rename":
      return 2;
    case "delete":
      return 3;
  }
}

function currentChangesetPatchset(changeset: Changeset) {
  if (!changeset.patchsets?.length) {
    return undefined;
  }
  return (
    changeset.patchsets.find(
      (patchset) => patchset.id === changeset.currentPatchsetId
    ) ?? changeset.patchsets[changeset.patchsets.length - 1]
  );
}

function fileEditToPendingEdit(edit: FileEdit): PendingEdit | null {
  const op = edit.op ?? "";
  const rawPath = edit.path?.trim() ?? "";

  if (!rawPath) {
    return null;
  }

  const path = normalizeRepositoryPath(rawPath);

  // The server normalizes write ops to "upsert" when it stores patchset edits,
  // so an adopted draft reports "upsert" rather than the original add/update.
  if (
    op === "add" ||
    op === "update" ||
    op === "modify" ||
    op === "upsert"
  ) {
    return {
      kind: "write",
      path,
      isNew: op === "add",
      blobId: edit.blobId,
      contentHash: edit.contentHash
    };
  }

  if (op === "mkdir") {
    return { kind: "mkdir", path };
  }

  if (op === "rename") {
    const rawOldPath = edit.oldPath?.trim() ?? "";
    if (!rawOldPath) {
      return null;
    }
    const oldPath = normalizeRepositoryPath(rawOldPath);
    return { kind: "rename", oldPath, path };
  }

  if (op === "delete") {
    return { kind: "delete", path };
  }

  return null;
}

function draftStatusLabel(status: DraftSaveStatus) {
  if (status === "adopting") {
    return "Loading draft...";
  }
  if (status === "saving") {
    return "Saving...";
  }
  if (status === "saved") {
    return "Saved";
  }
  if (status === "failed") {
    return "Save failed";
  }
  return "Not saved";
}

function pendingEditLabel(edit: PendingEdit) {
  if (edit.kind === "write") {
    return edit.isNew ? "new file" : "edited";
  }
  if (edit.kind === "mkdir") {
    return "new folder";
  }
  if (edit.kind === "rename") {
    return "renamed";
  }
  return "deleted";
}

function normalizePendingEdit(edit: PendingEdit): PendingEdit {
  if (edit.kind === "rename") {
    return {
      ...edit,
      oldPath: normalizeRepositoryPath(edit.oldPath),
      path: normalizeRepositoryPath(edit.path)
    };
  }
  return {
    ...edit,
    path: normalizeRepositoryPath(edit.path)
  };
}

function hasConflictingPendingPath(candidate: PendingEdit, next: PendingEdit) {
  if (next.kind === "rename") {
    if (candidate.kind === "rename") {
      return candidate.oldPath === next.oldPath;
    }
    return candidate.path === next.oldPath;
  }

  if (candidate.kind === "rename") {
    return candidate.oldPath === next.path;
  }

  return candidate.path === next.path;
}

function normalizeDirectoryPath(path: string) {
  const normalized = normalizeRepositoryPath(path);
  return normalized === "/" ? "" : normalized;
}

function errorMessageFromUnknown(error: unknown) {
  return error instanceof Error ? error.message : "The request failed.";
}

function utf8ToBase64(value: string) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (let index = 0; index < bytes.length; index += 1) {
    binary += String.fromCharCode(bytes[index]);
  }
  return window.btoa(binary);
}
