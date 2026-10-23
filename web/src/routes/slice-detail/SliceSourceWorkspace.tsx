import { cn } from "../../lib/cn";
import { SlicePanel } from "../../components/slices/SlicePageParts";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { entryKindLabel } from "../../components/source/sourceUtils";
import { DirectoryCreateControls, type PendingEdit } from "../../components/source/SliceEditing";
import { DirectoryHeader } from "./DirectoryHeader";
import { FolderWithReadme } from "./FolderWithReadme";
import { SliceDirectoryTable } from "./SliceDirectoryTable";
import { EditableFileView } from "./EditableFileView";
import { SourceSkeleton } from "./skeletons";
import type { BlameControl } from "../../components/source/SourceCodeViewer";
import type { SliceRef, TreeEntry } from "../../api/types";

interface SliceSourceWorkspaceProps {
  blame?: BlameControl;
  commitError: Error | null;
  commitId: string;
  directoryEntries: TreeEntry[];
  directoryError: Error | null;
  entry: TreeEntry | undefined;
  fileContent: string;
  fileData: string;
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
  // The README the folder shows (see useFolderReadme), found by the page,
  // which folds its file tree when there is one.
  readme?: TreeEntry;
  readmeLoading?: boolean;
  // The slice's root, which a phone shows as the README above the tree and
  // not as a folder listing or a skeleton of one.
  rootOnPhone?: boolean;
  selectedPath: string;
  createDirectory: string;
  sliceRef?: SliceRef;
}

export function SliceSourceWorkspace({
  blame,
  commitError,
  commitId,
  createDirectory,
  directoryEntries,
  directoryError,
  entry,
  fileContent,
  fileData,
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
  readme,
  readmeLoading = false,
  rootOnPhone = false,
  selectedPath,
  sliceRef
}: SliceSourceWorkspaceProps) {
  const skeleton = rootOnPhone ? (
    <div className="max-lg:hidden">
      <SourceSkeleton />
    </div>
  ) : (
    <SourceSkeleton />
  );

  if (isLatestLoading || isPathLoading) {
    return skeleton;
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
      return skeleton;
    }
    if (directoryError) {
      return (
        <SliceNotice title="Could not list directory" tone="error">
          {directoryError.message}
        </SliceNotice>
      );
    }

    if (readmeLoading) {
      return skeleton;
    }
    if (readme) {
      return (
        <FolderWithReadme
          commitId={commitId}
          createDirectory={createDirectory}
          includedPaths={includedPaths}
          onOpenHistory={onOpenHistory}
          onSelectPath={onSelectPath}
          onStageEdit={onStageEdit}
          readme={readme}
          selectedPath={selectedPath}
          sliceRef={sliceRef}
        />
      );
    }

    return (
      <SlicePanel className={cn("p-0", rootOnPhone && "max-lg:hidden")}>
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
      return skeleton;
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
        blame={blame}
        commitId={commitId}
        fileContent={fileContent}
        fileData={fileData}
        includedPaths={includedPaths}
        onOpenHistory={onOpenHistory}
        onSelectPath={onSelectPath}
        onStageEdit={onStageEdit}
        pendingEdits={pendingEdits}
        selectedPath={entry.path ?? selectedPath}
        sliceRef={sliceRef}
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
      <p className="mt-4 text-sm text-slate-600 dark:text-zinc-400">
        {entryKindLabel(entry.kind)} entries are visible in the navigator but do
        not have a preview in this view.
      </p>
    </SlicePanel>
  );
}
