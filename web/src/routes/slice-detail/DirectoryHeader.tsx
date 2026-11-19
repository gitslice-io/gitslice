import type { ReactNode } from "react";
import { useEffect, useState } from "react";

import {
  InlineRenameForm,
  NewFileForm,
  NewFolderForm,
  parentRepositoryPath,
  repositoryPathName,
  type PendingEdit
} from "../../components/source/SliceEditing";
import { ActionMenu, type ActionMenuItem } from "../../components/source/ActionMenu";
import { canCreateInPath } from "../../components/source/sourceUtils";
import { shortHash } from "../../lib/objectId";
import { cn } from "../../lib/cn";
import { useMobileDetailsClass } from "../../components/MobileDetails";
import { copyMoveMenuItems, useCopyMove } from "./CopyMove";

interface DirectoryHeaderProps {
  commitId: string;
  createDirectory?: string;
  includedPaths: string[];
  onOpenHistory?: () => void;
  onOpenPolicy?: () => void;
  onStageEdit?: (edit: PendingEdit) => void;
  selectedPath: string;
  // compact leaves out the commit line (it stays in the heading's tooltip), for
  // pages whose content, such as a README, should come first.
  compact?: boolean;
  actions?: ReactNode;
  children?: ReactNode;
}

const TOP_LEVEL_SLICE_FOLDER_TITLE =
  "Top-level slice folders are managed in the slice definition (Settings).";

type DirectoryActionMode = "" | "rename" | "new-file" | "new-folder";

export function DirectoryHeader({
  commitId,
  createDirectory,
  includedPaths,
  onOpenHistory,
  onOpenPolicy,
  onStageEdit,
  selectedPath,
  compact = false,
  actions,
  children
}: DirectoryHeaderProps) {
  const [mode, setMode] = useState<DirectoryActionMode>("");
  const copyMove = useCopyMove();
  const foldedOnPhone = useMobileDetailsClass();
  const targetCreateDir = createDirectory ?? selectedPath;
  const canCreate = Boolean(
    onStageEdit && targetCreateDir && canCreateInPath(includedPaths, targetCreateDir)
  );
  const canRename = Boolean(selectedPath && onStageEdit);
  const canModifySelectedPath = canModifyPath(includedPaths, selectedPath);
  const modifyDisabledTitle = canModifySelectedPath
    ? undefined
    : TOP_LEVEL_SLICE_FOLDER_TITLE;

  useEffect(() => {
    setMode("");
  }, [selectedPath, targetCreateDir]);

  function stageRename(name: string) {
    if (!onStageEdit) {
      return;
    }
    onStageEdit({
      kind: "rename",
      oldPath: selectedPath,
      path: joinRepositoryPath(parentRepositoryPath(selectedPath), name)
    });
    setMode("");
  }

  function stageCreate(edit: PendingEdit) {
    if (!onStageEdit) {
      return;
    }
    onStageEdit(edit);
    setMode("");
  }

  const menuItems: ActionMenuItem[] = [
    ...(canCreate
      ? [
          { label: "New file", onSelect: () => setMode("new-file") },
          { label: "New folder", onSelect: () => setMode("new-folder") }
        ]
      : []),
    ...(canRename
      ? [
          {
            label: "Rename",
            disabled: !canModifySelectedPath,
            onSelect: () => setMode("rename"),
            title: modifyDisabledTitle
          },
          ...copyMoveMenuItems(
            copyMove,
            { path: selectedPath, isDirectory: true },
            {
              canModifySource: canModifySelectedPath,
              disabledTitle: TOP_LEVEL_SLICE_FOLDER_TITLE
            }
          ),
          {
            label: "Delete",
            disabled: !canModifySelectedPath,
            onSelect: () => onStageEdit?.({ kind: "delete", path: selectedPath }),
            title: modifyDisabledTitle,
            tone: "danger" as const
          }
        ]
      : [])
  ];

  const ownMenu =
    menuItems.length > 0 ? (
      <ActionMenu items={menuItems} label="Directory actions" />
    ) : null;
  const rightActions = actions !== undefined ? actions : ownMenu;
  const policyButton = onOpenPolicy ? (
    <button
      className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
      onClick={onOpenPolicy}
      title="Owners, checks, triggers, and secrets for this folder"
      type="button"
    >
      Policy
    </button>
  ) : null;
  const historyButton = onOpenHistory ? (
    <button
      className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
      onClick={onOpenHistory}
      type="button"
    >
      History
    </button>
  ) : null;

  return (
    <div className={cn("border-b border-slate-200 dark:border-zinc-800 px-4 py-4 sm:px-5", mode === "" && foldedOnPhone)}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <h2
            className="break-all text-base font-semibold text-zinc-950 dark:text-zinc-50"
            title={compact ? `Commit ${commitId}` : undefined}
          >
            {selectedPath || "Slice root"}
          </h2>
          {compact ? null : (
            <p className="mt-1 hidden text-xs text-slate-500 dark:text-zinc-400 sm:block">
              Commit{" "}
              <span className="font-mono text-slate-700 dark:text-zinc-300" title={commitId}>
                {shortHash(commitId)}
              </span>
            </p>
          )}
        </div>
        {policyButton || historyButton || rightActions ? (
          <div className="flex shrink-0 items-center gap-2">
            {policyButton}
            {historyButton}
            {rightActions}
          </div>
        ) : null}
      </div>
      {children}
      {actions === undefined && mode === "rename" && canModifySelectedPath ? (
        <div className="mt-3">
          <InlineRenameForm
            directoryPath={parentRepositoryPath(selectedPath)}
            onCancel={() => setMode("")}
            onSave={stageRename}
            originalName={repositoryPathName(selectedPath)}
          />
        </div>
      ) : null}
      {actions === undefined && mode === "new-file" && canCreate ? (
        <NewFileForm
          directoryPath={targetCreateDir}
          onCancel={() => setMode("")}
          onCreate={stageCreate}
        />
      ) : null}
      {actions === undefined && mode === "new-folder" && canCreate ? (
        <NewFolderForm
          directoryPath={targetCreateDir}
          onCancel={() => setMode("")}
          onCreate={stageCreate}
        />
      ) : null}
    </div>
  );
}

export function canModifyPath(includedPaths: string[], path: string): boolean {
  const normalizedPath = path.replace(/^\/+|\/+$/g, "");
  return includedPaths.some((includedPath) =>
    pathCoversCurrentPath(includedPath, normalizedPath)
  ) && !isIncludedRoot(includedPaths, path);
}

function pathCoversCurrentPath(includedPath: string, currentPath: string) {
  const normalizedIncluded = includedPath.replace(/^\/+|\/+$/g, "");
  if (normalizedIncluded === "") {
    return true;
  }
  return currentPath === normalizedIncluded || currentPath.startsWith(`${normalizedIncluded}/`);
}

function isIncludedRoot(includedPaths: string[], path: string): boolean {
  const normalizedPath = path.replace(/^\/+|\/+$/g, "");
  return includedPaths.some(
    (includedPath) => includedPath.replace(/^\/+|\/+$/g, "") === normalizedPath
  );
}

export function joinRepositoryPath(...parts: string[]) {
  return "/" + parts.filter(Boolean).join("/");
}