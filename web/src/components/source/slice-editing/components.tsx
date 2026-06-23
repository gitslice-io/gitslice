import { Link, useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";

import type { ApiClient } from "../../../api/useApi";
import { shortChangesetId } from "../../../lib/objectId";
import { ActionMenu } from "../ActionMenu";
import { canCreateInPath } from "../sourceUtils";

import type { PendingEdit } from "./pendingEdits";
import type { DraftChangesetController } from "./useDraftChangesetController";
import { draftStatusLabel, errorMessageFromUnknown, pendingEditLabel } from "./draftChangesetHelpers";
import { pendingEditKey } from "./pendingEdits";
import { joinRepositoryPath, validateEntryName } from "./paths";

export function DraftChangesetPanel({
  actionStatus,
  changesetId,
  changesetLabel,
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
  const shareId = shortChangesetId(changesetId);
  const detailId = shareId || changesetId;
  const visibleLabel = changesetLabel || detailId;
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
        to: "/cs/$id",
        params: { id: shortChangesetId(id) }
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
                to="/cs/$id"
              >
                {visibleLabel}
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

interface DirectoryCreateControlsProps {
  directoryPath: string;
  includedPaths: string[];
  onStageEdit(edit: PendingEdit): void;
}

export function DirectoryCreateControls({
  directoryPath,
  includedPaths,
  onStageEdit
}: DirectoryCreateControlsProps) {
  const [mode, setMode] = useState<"file" | "folder" | "">("");

  if (!directoryPath) {
    return (
      <div className="border-b border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-600 sm:px-5">
        Open a folder to add files or folders here.
      </div>
    );
  }

  if (!canCreateInPath(includedPaths, directoryPath)) {
    return (
      <div className="border-b border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-600 sm:px-5">
        You can't add items at the slice top level. Open a folder inside one of
        the slice's included paths, or edit the included paths from the slice's
        Settings.
      </div>
    );
  }

  return (
    <div className="border-b border-slate-200 bg-slate-50 px-4 py-4 sm:px-5">
      <div className="flex min-w-0 items-start justify-between gap-3">
        <p className="min-w-0 break-all text-xs text-slate-500">
          New items are created in{" "}
          <span className="font-mono text-slate-700">{directoryPath}</span>
        </p>
        <ActionMenu
          items={[
            {
              label: "New file",
              onSelect: () => setMode("file")
            },
            {
              label: "New folder",
              onSelect: () => setMode("folder")
            }
          ]}
          label="Create item"
        />
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

interface InlineRenameFormProps {
  directoryPath?: string;
  onCancel(): void;
  onSave(name: string): void;
  originalName: string;
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