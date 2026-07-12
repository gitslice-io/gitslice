import { useEffect, useState } from "react";

import {
  InlineRenameForm,
  pendingWriteForPath,
  validateEntryName,
  parentRepositoryPath,
  repositoryPathName,
  type PendingEdit
} from "../../components/source/SliceEditing";
import { ActionMenu } from "../../components/source/ActionMenu";
import { SourceCodeViewer } from "../../components/source/SourceCodeViewer";
import { SlicePanel } from "../../components/slices/SlicePageParts";
import { canModifyPath, joinRepositoryPath } from "./DirectoryHeader";

const TOP_LEVEL_SLICE_FOLDER_TITLE =
  "Top-level slice folders are managed in the slice definition (Settings).";

interface EditableFileViewProps {
  commitId: string;
  fileContent: string;
  includedPaths: string[];
  onOpenHistory(): void;
  onStageEdit?: (edit: PendingEdit) => void;
  pendingEdits: PendingEdit[];
  selectedPath: string;
}

export function EditableFileView({
  commitId,
  fileContent,
  includedPaths,
  onOpenHistory,
  onStageEdit,
  pendingEdits,
  selectedPath
}: EditableFileViewProps) {
  const pendingWrite = pendingWriteForPath(pendingEdits, selectedPath);
  const displayedContent = pendingWrite?.content ?? fileContent;
  const [isEditing, setIsEditing] = useState(false);
  const [isRenaming, setIsRenaming] = useState(false);
  const [draft, setDraft] = useState(displayedContent);
  const [renameError, setRenameError] = useState("");
  const canModifySelectedPath = canModifyPath(includedPaths, selectedPath);
  const modifyDisabledTitle = canModifySelectedPath
    ? undefined
    : TOP_LEVEL_SLICE_FOLDER_TITLE;

  useEffect(() => {
    setIsEditing(false);
    setIsRenaming(false);
    setRenameError("");
    setDraft(displayedContent);
  }, [displayedContent, selectedPath]);

  function saveEdit() {
    if (!onStageEdit) {
      return;
    }
    onStageEdit({
      kind: "write",
      path: selectedPath,
      content: draft,
      isNew: false
    });
    setIsEditing(false);
  }

  function saveRename(name: string) {
    if (!onStageEdit) {
      return;
    }
    const validationError = validateEntryName(name);
    if (validationError) {
      setRenameError(validationError);
      return;
    }
    onStageEdit({
      kind: "rename",
      oldPath: selectedPath,
      path: joinRepositoryPath(parentRepositoryPath(selectedPath), name)
    });
    setIsRenaming(false);
    setRenameError("");
  }

  return (
    <div className="space-y-3">
      <SlicePanel className="p-0">
        <div className="border-b border-slate-200 dark:border-zinc-800 px-4 py-4 sm:px-5">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0 flex-1">
              <h2 className="break-all text-base font-semibold text-zinc-950 dark:text-zinc-50">
                {selectedPath || "Slice root"}
              </h2>
              <p className="mt-1 hidden text-xs text-slate-500 dark:text-zinc-400 sm:block">
                Commit{" "}
                <span className="font-mono text-slate-700 dark:text-zinc-300" title={commitId}>
                  {commitId.slice(0, 8)}
                </span>
              </p>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              {onOpenHistory ? (
                <button
                  className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
                  onClick={onOpenHistory}
                  type="button"
                >
                  History
                </button>
              ) : null}
              {onStageEdit ? (
                <ActionMenu
                  items={[
                    {
                      label: "Edit",
                      onSelect: () => {
                        setDraft(displayedContent);
                        setIsEditing(true);
                      }
                    },
                    {
                      label: "Rename",
                      disabled: !canModifySelectedPath,
                      onSelect: () => setIsRenaming(true),
                      title: modifyDisabledTitle
                    },
                    {
                      label: "Delete",
                      disabled: !canModifySelectedPath,
                      onSelect: () =>
                        onStageEdit({ kind: "delete", path: selectedPath }),
                      title: modifyDisabledTitle,
                      tone: "danger"
                    }
                  ]}
                  label="File actions"
                />
              ) : null}
            </div>
          </div>
          {isRenaming && canModifySelectedPath ? (
            <div className="mt-3">
              <InlineRenameForm
                directoryPath={parentRepositoryPath(selectedPath)}
                onCancel={() => {
                  setIsRenaming(false);
                  setRenameError("");
                }}
                onSave={saveRename}
                originalName={repositoryPathName(selectedPath)}
              />
              {renameError ? (
                <p className="mt-2 text-sm text-rose-700 dark:text-rose-300">{renameError}</p>
              ) : null}
            </div>
          ) : null}
        </div>
        {isEditing ? (
          <div className="p-4 sm:p-5">
            <label className="grid gap-2 text-sm font-medium text-zinc-800 dark:text-zinc-200">
              File content
              <textarea
                className="min-h-80 w-full min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 font-mono text-sm leading-6 text-zinc-950 dark:text-zinc-50 outline-none transition placeholder:text-slate-400 dark:placeholder:text-zinc-500 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700 sm:min-h-[28rem]"
                onChange={(event) => setDraft(event.target.value)}
                value={draft}
              />
            </label>
            <div className="mt-4 flex flex-wrap gap-2">
              <button
                className="rounded-md bg-zinc-950 dark:bg-zinc-100 px-3 py-2 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98]"
                onClick={saveEdit}
                type="button"
              >
                Save
              </button>
              <button
                className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
                onClick={() => {
                  setDraft(displayedContent);
                  setIsEditing(false);
                }}
                type="button"
              >
                Cancel
              </button>
            </div>
          </div>
        ) : null}
      </SlicePanel>
      {!isEditing ? (
        <SourceCodeViewer code={displayedContent} fill path={selectedPath} />
      ) : null}
    </div>
  );
}