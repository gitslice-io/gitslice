import type { SliceRef, TreeEntry } from "../../api/types";
import { DirectoryCreateControls, type PendingEdit } from "../../components/source/SliceEditing";
import { SlicePanel } from "../../components/slices/SlicePageParts";
import { DirectoryHeader } from "./DirectoryHeader";
import { FolderReadme } from "./FolderReadme";
import { normalizeRepositoryPath } from "../../components/source/sourceUtils";

// nestedLabel is the path of a README from the folder that shows it, when it
// lives in a folder below (see findNestedReadme); undefined for its own.
export function nestedLabel(folder: string, readme: TreeEntry): string | undefined {
  const dir = normalizeRepositoryPath(folder);
  const path = normalizeRepositoryPath(readme.path ?? "");
  const prefix = dir === "/" ? "/" : `${dir}/`;
  if (!path.startsWith(prefix)) {
    return undefined;
  }
  const relative = path.slice(prefix.length);
  return relative.includes("/") ? relative : undefined;
}

interface FolderWithReadmeProps {
  commitId: string;
  createDirectory: string;
  includedPaths: string[];
  onOpenHistory(): void;
  onSelectPath(path: string): void;
  onStageEdit?: (edit: PendingEdit) => void;
  readme: TreeEntry;
  selectedPath: string;
  sliceRef: SliceRef | undefined;
}

// FolderWithReadme is a folder page whose README is its introduction: the README
// takes the whole page under a slim header. The folder's own files are not
// listed beside it: the file tree, folded until asked for, has them.
export function FolderWithReadme({
  commitId,
  createDirectory,
  includedPaths,
  onOpenHistory,
  onSelectPath,
  onStageEdit,
  readme,
  selectedPath,
  sliceRef
}: FolderWithReadmeProps) {
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
      {onStageEdit ? (
        <DirectoryCreateControls
          directoryPath={createDirectory}
          includedPaths={includedPaths}
          onStageEdit={onStageEdit}
        />
      ) : null}
      <FolderReadme
        canEdit={Boolean(onStageEdit)}
        commitId={commitId}
        entry={readme}
        includedPaths={includedPaths}
        label={nestedLabel(selectedPath, readme)}
        onSelectPath={onSelectPath}
        sliceRef={sliceRef}
      />
    </SlicePanel>
  );
}
