import { useState, type FormEvent } from "react";

import { Button } from "../../ui/Button";
import { Input } from "../../ui/Input";
import { ActionMenu } from "../ActionMenu";
import { canCreateInPath } from "../sourceUtils";
import { joinRepositoryPath, validateEntryName } from "./paths";
import type { PendingEdit } from "./pendingEdits";

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
      <div className="border-b border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-4 py-3 text-sm text-slate-600 dark:text-zinc-400 sm:px-5">
        Open a folder to add files or folders here.
      </div>
    );
  }

  if (!canCreateInPath(includedPaths, directoryPath)) {
    return (
      <div className="border-b border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-4 py-3 text-sm text-slate-600 dark:text-zinc-400 sm:px-5">
        You can&apos;t add items at the slice top level. Open a folder inside one of
        the slice&apos;s included paths, or edit the included paths from the slice&apos;s
        Settings.
      </div>
    );
  }

  const stageAndClose = (edit: PendingEdit) => {
    onStageEdit(edit);
    setMode("");
  };

  return (
    <div className="border-b border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-4 py-4 sm:px-5">
      <div className="flex min-w-0 items-start justify-between gap-3">
        <p className="min-w-0 break-all text-xs text-slate-500 dark:text-zinc-400">
          New items are created in{" "}
          <span className="font-mono text-slate-700 dark:text-zinc-300">{directoryPath}</span>
        </p>
        <ActionMenu
          items={[
            { label: "New file", onSelect: () => setMode("file") },
            { label: "New folder", onSelect: () => setMode("folder") }
          ]}
          label="Create item"
        />
      </div>

      {mode === "file" ? (
        <NewFileForm
          directoryPath={directoryPath}
          onCancel={() => setMode("")}
          onCreate={stageAndClose}
        />
      ) : null}

      {mode === "folder" ? (
        <NewFolderForm
          directoryPath={directoryPath}
          onCancel={() => setMode("")}
          onCreate={stageAndClose}
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
    if (!nameError) {
      onSave(name.trim());
    }
  }

  return (
    <form className="grid gap-2" onSubmit={submit}>
      <div className="flex min-w-0 flex-wrap gap-2">
        <Input
          className="flex-1"
          onChange={(event) => setName(event.target.value)}
          value={name}
        />
        <Button disabled={Boolean(nameError)} size="sm" type="submit" variant="primary">
          Save
        </Button>
        <Button onClick={onCancel} size="sm" variant="secondary">
          Cancel
        </Button>
      </div>
      {directoryPath !== undefined ? (
        <EntryPathPreview directoryPath={directoryPath} name={name} verb="Renames to" />
      ) : nameError && name.trim() ? (
        <p className="text-xs text-rose-700 dark:text-rose-300">{nameError}</p>
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
    return <p className="text-xs text-rose-700 dark:text-rose-300">{error}</p>;
  }
  return (
    <p className="break-all text-xs text-slate-500 dark:text-zinc-400">
      {verb}{" "}
      <span className="font-mono text-slate-700 dark:text-zinc-300">
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
    if (!nameError) {
      onCreate({
        kind: "write",
        path: joinRepositoryPath(directoryPath, name),
        content,
        isNew: true
      });
    }
  }

  return (
    <form className="mt-4 grid gap-3" onSubmit={submit}>
      <label className="grid gap-2 text-sm font-medium text-zinc-800 dark:text-zinc-200">
        File name
        <Input
          onChange={(event) => setName(event.target.value)}
          placeholder="notes.md or docs/notes.md"
          value={name}
        />
      </label>
      <EntryPathPreview directoryPath={directoryPath} name={name} verb="Creates" />
      <label className="grid gap-2 text-sm font-medium text-zinc-800 dark:text-zinc-200">
        Content
        <textarea
          className="min-h-40 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 font-mono text-sm leading-6 text-zinc-950 dark:text-zinc-50 outline-none transition placeholder:text-slate-400 dark:placeholder:text-zinc-500 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700"
          onChange={(event) => setContent(event.target.value)}
          value={content}
        />
      </label>
      <div className="flex flex-wrap gap-2">
        <Button disabled={Boolean(nameError)} size="md" type="submit" variant="primary">
          Save
        </Button>
        <Button onClick={onCancel} size="md" variant="secondary">
          Cancel
        </Button>
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
    if (!nameError) {
      onCreate({ kind: "mkdir", path: joinRepositoryPath(directoryPath, name) });
    }
  }

  return (
    <form className="mt-4 grid gap-3 sm:max-w-md" onSubmit={submit}>
      <label className="grid gap-2 text-sm font-medium text-zinc-800 dark:text-zinc-200">
        Folder name
        <Input
          onChange={(event) => setName(event.target.value)}
          placeholder="docs or docs/images"
          value={name}
        />
      </label>
      <EntryPathPreview directoryPath={directoryPath} name={name} verb="Creates" />
      <div className="flex flex-wrap gap-2">
        <Button disabled={Boolean(nameError)} size="md" type="submit" variant="primary">
          Save
        </Button>
        <Button onClick={onCancel} size="md" variant="secondary">
          Cancel
        </Button>
      </div>
    </form>
  );
}
