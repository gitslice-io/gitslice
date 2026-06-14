import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type FormEvent } from "react";

import type { FileEdit, SliceRef } from "../../api/types";
import { type ApiClient } from "../../api/useApi";
import { normalizeRepositoryPath } from "./sourceUtils";

export type PendingEdit =
  | { kind: "write"; path: string; content: string; isNew: boolean }
  | { kind: "mkdir"; path: string }
  | { kind: "rename"; oldPath: string; path: string }
  | { kind: "delete"; path: string };

interface PendingChangesTrayProps {
  api: ApiClient;
  commitId: string;
  edits: PendingEdit[];
  onClear(): void;
  onRemove(key: string): void;
  sliceLabel: string;
  sliceRef: SliceRef | undefined;
}

interface DirectoryCreateControlsProps {
  directoryPath: string;
  onStageEdit(edit: PendingEdit): void;
}

interface InlineRenameFormProps {
  onCancel(): void;
  onSave(name: string): void;
  originalName: string;
}

export function PendingChangesTray({
  api,
  commitId,
  edits,
  onClear,
  onRemove,
  sliceLabel,
  sliceRef
}: PendingChangesTrayProps) {
  const navigate = useNavigate();
  const defaultTitle = `Web edits to ${sliceLabel}`;
  const [title, setTitle] = useState(defaultTitle);

  useEffect(() => {
    setTitle(defaultTitle);
  }, [defaultTitle]);

  const createMutation = useMutation({
    mutationFn: async () => {
      if (!commitId) {
        throw new Error("Latest global state did not return a commit id.");
      }
      if (!sliceRef?.account || !sliceRef?.slice) {
        throw new Error("Authoring slice is missing account or slice.");
      }
      if (!edits.length) {
        throw new Error("Stage at least one file edit.");
      }

      const fileEdits = await preparePendingFileEdits(api, edits, sliceRef);
      const changeset = await api.createChangeset({
        authoringSlice: sliceRef,
        baseCommitId: commitId,
        title: title.trim() || defaultTitle,
        description: ""
      });

      if (!changeset.id) {
        throw new Error("CreateChangeset did not return a changeset id.");
      }

      await api.updateChangeset({
        changesetId: changeset.id,
        expectedCurrentPatchsetId: changeset.currentPatchsetId,
        baseCommitId: commitId,
        fileEdits
      });

      return changeset;
    },
    onSuccess: (changeset) => {
      const id = changeset.handle || changeset.id;
      if (!id) {
        return;
      }
      onClear();
      void navigate({
        to: "/changesets/$id",
        params: { id }
      });
    }
  });

  if (!edits.length) {
    return null;
  }

  return (
    <section className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <h2 className="text-base font-semibold text-zinc-950">
            Pending changes
          </h2>
          <p className="mt-1 text-sm text-slate-600">
            These edits are staged in this browser until you create a draft
            changeset.
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          <button
            className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-60"
            disabled={createMutation.isPending}
            onClick={onClear}
            type="button"
          >
            Discard all
          </button>
          <button
            className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-60"
            disabled={createMutation.isPending}
            onClick={() => createMutation.mutate()}
            type="button"
          >
            {createMutation.isPending ? "Creating..." : "Create changeset"}
          </button>
        </div>
      </div>

      <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
        Title
        <input
          className="h-10 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
          disabled={createMutation.isPending}
          onChange={(event) => setTitle(event.target.value)}
          placeholder={defaultTitle}
          value={title}
        />
      </label>

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
                disabled={createMutation.isPending}
                onClick={() => onRemove(key)}
                type="button"
              >
                Remove
              </button>
            </li>
          );
        })}
      </ul>

      {createMutation.error ? (
        <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-900">
          {createMutation.error instanceof Error
            ? createMutation.error.message
            : "Could not create changeset."}
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

  return (
    <div className="border-b border-slate-200 bg-slate-50 px-5 py-4">
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
  onCancel,
  onSave,
  originalName
}: InlineRenameFormProps) {
  const [name, setName] = useState(originalName);
  const [error, setError] = useState("");

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const validationError = validateEntryName(name);
    if (validationError) {
      setError(validationError);
      return;
    }
    onSave(name.trim());
  }

  return (
    <form className="grid gap-2" onSubmit={submit}>
      <div className="flex min-w-0 flex-wrap gap-2">
        <input
          className="h-9 min-w-40 rounded-md border border-slate-300 bg-white px-2.5 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
          onChange={(event) => {
            setName(event.target.value);
            setError("");
          }}
          value={name}
        />
        <button
          className="rounded-md bg-zinc-950 px-2.5 py-1.5 text-xs font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
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
      {error ? <p className="text-xs text-rose-700">{error}</p> : null}
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

export function validateEntryName(name: string) {
  const trimmed = name.trim();
  if (!trimmed) {
    return "Enter a name.";
  }
  if (trimmed.includes("/") || trimmed.includes("\\")) {
    return "Names cannot contain path separators.";
  }
  return "";
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
  const [error, setError] = useState("");

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const validationError = validateEntryName(name);
    if (validationError) {
      setError(validationError);
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
          onChange={(event) => {
            setName(event.target.value);
            setError("");
          }}
          placeholder="notes.md"
          value={name}
        />
      </label>
      <label className="grid gap-2 text-sm font-medium text-zinc-800">
        Content
        <textarea
          className="min-h-40 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm leading-6 text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
          onChange={(event) => setContent(event.target.value)}
          value={content}
        />
      </label>
      {error ? <p className="text-sm text-rose-700">{error}</p> : null}
      <div className="flex flex-wrap gap-2">
        <button
          className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
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
  const [error, setError] = useState("");

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const validationError = validateEntryName(name);
    if (validationError) {
      setError(validationError);
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
          onChange={(event) => {
            setName(event.target.value);
            setError("");
          }}
          placeholder="docs"
          value={name}
        />
      </label>
      {error ? <p className="text-sm text-rose-700">{error}</p> : null}
      <div className="flex flex-wrap gap-2">
        <button
          className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
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

function utf8ToBase64(value: string) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (let index = 0; index < bytes.length; index += 1) {
    binary += String.fromCharCode(bytes[index]);
  }
  return window.btoa(binary);
}
