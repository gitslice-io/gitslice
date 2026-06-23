import { SlicePanel } from "../../components/slices/SlicePageParts";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { entryKindLabel } from "../../components/source/sourceUtils";
import { DirectoryCreateControls, type PendingEdit } from "../../components/source/SliceEditing";
import { DirectoryHeader } from "./DirectoryHeader";
import { SliceDirectoryTable } from "./SliceDirectoryTable";
import { EditableFileView } from "./EditableFileView";
import { SourceSkeleton } from "./skeletons";
import type { TreeEntry } from "../../api/types";

interface SliceSourceWorkspaceProps {
  commitError: Error | null;
  commitId: string;
  directoryEntries: TreeEntry[];
  directoryError: Error | null;
  entry: TreeEntry | undefined;
  fileContent: string;
  fileError: Error | null;
  includedPaths: string[];
  isDirectoryLoading: boolean;
  isFileLoading: boolean;
  isLatestLoading: boolean;
  isPathLoading: boolean;
  onOpenHistory(): void;
  onSelectPath(path: string): void;
  onStageEdit?: (edit: PendingEdit) => void;
  pathError: Error | null;
  pendingEdits: PendingEdit[];
  selectedPath: string;
  createDirectory: string;
}

export function SliceSourceWorkspace({
  commitError,
  commitId,
  createDirectory,
  directoryEntries,
  directoryError,
  entry,
  fileContent,
  fileError,
  includedPaths,
  isDirectoryLoading,
  isFileLoading,
  isLatestLoading,
  isPathLoading,
  onOpenHistory,
  onSelectPath,
  onStageEdit,
  pathError,
  pendingEdits,
  selectedPath
}: SliceSourceWorkspaceProps) {
  if (isLatestLoading || isPathLoading) {
    return <SourceSkeleton />;
  }

  if (commitError) {
    return (
      <SliceNotice title="Could not resolve latest source" tone="error">
        {commitError.message}
      </SliceNotice>
    );
  }

  if (!commitId) {
    return (
      <SliceNotice title="Missing commit">
        The latest global state did not return a commit id.
      </SliceNotice>
    );
  }

  if (pathError) {
    return (
      <SliceNotice title="Could not resolve path" tone="error">
        {pathError.message}
      </SliceNotice>
    );
  }

  if (!entry) {
    return (
      <SliceNotice title="Path not found">
        No source entry was returned for {selectedPath || "slice root"}.
      </SliceNotice>
    );
  }

  if (entry.kind === "ENTRY_KIND_DIRECTORY") {
    if (isDirectoryLoading) {
      return <SourceSkeleton />;
    }
    if (directoryError) {
      return (
        <SliceNotice title="Could not list directory" tone="error">
          {directoryError.message}
        </SliceNotice>
      );
    }

    return (
      <SlicePanel className="p-0">
        <DirectoryHeader
          commitId={commitId}
          includedPaths={includedPaths}
          onOpenHistory={onOpenHistory}
          onStageEdit={onStageEdit}
          selectedPath={selectedPath}
        />
        {onStageEdit ? (
          <DirectoryCreateControls
            directoryPath={createDirectory}
            includedPaths={includedPaths}
            onStageEdit={onStageEdit}
          />
        ) : null}
        <SliceDirectoryTable
          entries={directoryEntries}
          includedPaths={includedPaths}
          onSelectPath={onSelectPath}
          onStageEdit={onStageEdit}
        />
      </SlicePanel>
    );
  }

  if (entry.kind === "ENTRY_KIND_FILE") {
    if (isFileLoading) {
      return <SourceSkeleton />;
    }
    if (fileError) {
      return (
        <SliceNotice title="Could not read file" tone="error">
          {fileError.message}
        </SliceNotice>
      );
    }

    return (
      <EditableFileView
        commitId={commitId}
        fileContent={fileContent}
        includedPaths={includedPaths}
        onOpenHistory={onOpenHistory}
        onStageEdit={onStageEdit}
        pendingEdits={pendingEdits}
        selectedPath={entry.path ?? selectedPath}
      />
    );
  }

  return (
    <SlicePanel>
      <DirectoryHeader
        commitId={commitId}
        includedPaths={includedPaths}
        onOpenHistory={onOpenHistory}
        selectedPath={selectedPath}
      />
      <p className="mt-4 text-sm text-slate-600">
        {entryKindLabel(entry.kind)} entries are visible in the navigator but do
        not have a preview in this view.
      </p>
    </SlicePanel>
  );
}