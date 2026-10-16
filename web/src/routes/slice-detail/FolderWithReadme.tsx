import { useState } from "react";

import type { SliceRef, TreeEntry } from "../../api/types";
import { DirectoryCreateControls, type PendingEdit } from "../../components/source/SliceEditing";
import { SlicePanel } from "../../components/slices/SlicePageParts";
import { cn } from "../../lib/cn";
import { DirectoryHeader } from "./DirectoryHeader";
import { FolderReadme } from "./FolderReadme";
import { SliceDirectoryTable } from "./SliceDirectoryTable";

interface FolderWithReadmeProps {
  commitId: string;
  createDirectory: string;
  entries: TreeEntry[];
  includedPaths: string[];
  onOpenHistory(): void;
  onSelectPath(path: string): void;
  onStageEdit?: (edit: PendingEdit) => void;
  readme: TreeEntry;
  selectedPath: string;
  sliceRef: SliceRef | undefined;
}

// FolderWithReadme is a folder page whose README is its introduction: the README
// takes the main column under a slim header, and the files sit in a narrow
// column beside it (on a phone, under it, folded until asked for).
export function FolderWithReadme({
  commitId,
  createDirectory,
  entries,
  includedPaths,
  onOpenHistory,
  onSelectPath,
  onStageEdit,
  readme,
  selectedPath,
  sliceRef
}: FolderWithReadmeProps) {
  const [filesOpen, setFilesOpen] = useState(false);
  return (
    <SlicePanel className="p-0">
      <DirectoryHeader
        commitId={commitId}
        compact
        includedPaths={includedPaths}
        onOpenHistory={onOpenHistory}
        onStageEdit={onStageEdit}
        selectedPath={selectedPath}
      />
      <div className="grid lg:grid-cols-[minmax(0,1fr)_20rem]">
        <FolderReadme
          canEdit={Boolean(onStageEdit)}
          commitId={commitId}
          entry={readme}
          onSelectPath={onSelectPath}
          sliceRef={sliceRef}
        />
        <aside
          aria-label="Files"
          className="min-w-0 border-t border-slate-200 dark:border-zinc-800 lg:border-l lg:border-t-0"
        >
          <button
            aria-expanded={filesOpen}
            className="flex w-full items-center justify-between px-4 py-3 text-left text-sm font-semibold text-zinc-950 dark:text-zinc-50 lg:hidden"
            onClick={() => setFilesOpen((open) => !open)}
            type="button"
          >
            <span>Files ({entries.length})</span>
            <span className="text-xs font-normal text-slate-500 dark:text-zinc-400">
              {filesOpen ? "Hide" : "Show"}
            </span>
          </button>
          <h3 className="hidden border-b border-slate-200 dark:border-zinc-800 px-4 py-2.5 text-sm font-semibold text-zinc-950 dark:text-zinc-50 lg:block">
            Files <span className="font-normal text-slate-500 dark:text-zinc-400">({entries.length})</span>
          </h3>
          <div className={cn(filesOpen ? "block" : "hidden", "lg:block")}>
            {onStageEdit ? (
              <DirectoryCreateControls
                directoryPath={createDirectory}
                includedPaths={includedPaths}
                onStageEdit={onStageEdit}
              />
            ) : null}
            <SliceDirectoryTable
              compact
              entries={entries}
              includedPaths={includedPaths}
              onSelectPath={onSelectPath}
              onStageEdit={onStageEdit}
            />
          </div>
        </aside>
      </div>
    </SlicePanel>
  );
}
