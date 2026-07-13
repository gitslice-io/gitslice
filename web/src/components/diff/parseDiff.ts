export type DiffLineKind = "context" | "add" | "del" | "hunk" | "meta";

export interface DiffLine {
  kind: DiffLineKind;
  text: string;
  content: string;
  oldNumber?: number;
  newNumber?: number;
}

export interface DiffRow {
  left?: DiffLine;
  right?: DiffLine;
  kind: "context" | "add" | "del" | "replace" | "hunk" | "meta";
  hunkText?: string;
}

export type FileChangeKind = "added" | "deleted" | "modified" | "renamed";

export interface DiffFile {
  id: string;
  path: string;
  oldPath?: string;
  changeKind: FileChangeKind;
  additions: number;
  deletions: number;
  lines: DiffLine[];
  rows: DiffRow[];
}

export function diffFileId(path: string) {
  return `diff-file-${encodeURIComponent(path)}`;
}

interface DiffSectionDraft {
  deletedFile: boolean;
  diffNewPath?: string;
  diffOldPath?: string;
  lines: string[];
  newFile: boolean;
  newPath?: string;
  oldPath?: string;
  renameFrom?: string;
  renameTo?: string;
}

export function parseDiff(diff: string, changedPaths: string[]): DiffFile[] {
  const normalized = diff.replace(/\r\n/g, "\n");
  if (!normalized.trim()) {
    return [];
  }

  const rawLines = normalized.split("\n");
  if (rawLines[rawLines.length - 1] === "") {
    rawLines.pop();
  }

  const drafts = splitDiffSections(rawLines);

  return drafts
    .filter((section) => section.lines.length > 0)
    .map((section, index) => buildDiffFile(section, changedPaths, index));
}

function splitDiffSections(lines: string[]) {
  const drafts: DiffSectionDraft[] = [];
  let current: DiffSectionDraft | null = null;

  const startSection = () => {
    current = {
      deletedFile: false,
      lines: [],
      newFile: false
    };
    drafts.push(current);
  };

  lines.forEach((line, index) => {
    const nextLine = lines[index + 1] ?? "";
    const startsGitFile = line.startsWith("diff --git ");
    const startsUnifiedFile =
      line.startsWith("--- ") &&
      nextLine.startsWith("+++ ") &&
      (!current || sectionHasBody(current));

    if (startsGitFile || startsUnifiedFile || !current) {
      startSection();
    }

    if (!current) {
      return;
    }

    updateSectionMetadata(current, line);
    current.lines.push(line);
  });

  return drafts;
}

function buildDiffFile(
  section: DiffSectionDraft,
  changedPaths: string[],
  index: number
): DiffFile {
  const parsedLines = parseDiffLines(section.lines);
  const oldPath = section.renameFrom || section.oldPath || section.diffOldPath;
  const path =
    section.renameTo ||
    section.newPath ||
    (section.deletedFile ? oldPath : section.diffNewPath) ||
    oldPath ||
    changedPaths[index] ||
    `File ${index + 1}`;
  const displayOldPath =
    oldPath && oldPath !== path ? oldPath : section.renameFrom;

  return {
    additions: parsedLines.lines.filter((line) => line.kind === "add").length,
    changeKind: changeKindForSection(section, displayOldPath, path),
    deletions: parsedLines.lines.filter((line) => line.kind === "del").length,
    id: diffFileId(path),
    lines: parsedLines.lines,
    oldPath: displayOldPath,
    path,
    rows: parsedLines.rows
  };
}

function parseDiffLines(lines: string[]) {
  const diffLines: DiffLine[] = [];
  const rows: DiffRow[] = [];
  let oldLine = 0;
  let newLine = 0;
  let pendingAdds: DiffLine[] = [];
  let pendingDels: DiffLine[] = [];

  const flushChangedRows = () => {
    const rowCount = Math.max(pendingDels.length, pendingAdds.length);

    for (let index = 0; index < rowCount; index += 1) {
      const left = pendingDels[index];
      const right = pendingAdds[index];

      if (left && right) {
        rows.push({ kind: "replace", left, right });
      } else if (left) {
        rows.push({ kind: "del", left });
      } else if (right) {
        rows.push({ kind: "add", right });
      }
    }

    pendingAdds = [];
    pendingDels = [];
  };

  lines.forEach((line) => {
    const hunk = parseHunkHeader(line);

    if (hunk) {
      flushChangedRows();
      oldLine = hunk.oldStart;
      newLine = hunk.newStart;
      const diffLine: DiffLine = {
        content: line,
        kind: "hunk",
        text: line
      };
      diffLines.push(diffLine);
      rows.push({ hunkText: line, kind: "hunk" });
      return;
    }

    if (line.startsWith("\\")) {
      const diffLine: DiffLine = {
        content: line,
        kind: "meta",
        text: line
      };
      diffLines.push(diffLine);
      return;
    }

    if (isAdditionLine(line)) {
      const diffLine: DiffLine = {
        content: line.slice(1),
        kind: "add",
        newNumber: newLine,
        text: line
      };
      newLine += 1;
      diffLines.push(diffLine);
      pendingAdds.push(diffLine);
      return;
    }

    if (isDeletionLine(line)) {
      const diffLine: DiffLine = {
        content: line.slice(1),
        kind: "del",
        oldNumber: oldLine,
        text: line
      };
      oldLine += 1;
      diffLines.push(diffLine);
      pendingDels.push(diffLine);
      return;
    }

    if (line.startsWith(" ") || isInsideHunk(oldLine, newLine)) {
      flushChangedRows();
      const diffLine: DiffLine = {
        content: line.startsWith(" ") ? line.slice(1) : line,
        kind: "context",
        newNumber: newLine,
        oldNumber: oldLine,
        text: line
      };
      oldLine += 1;
      newLine += 1;
      diffLines.push(diffLine);
      rows.push({ kind: "context", left: diffLine, right: diffLine });
      return;
    }

    flushChangedRows();
    const diffLine: DiffLine = {
      content: line,
      kind: "meta",
      text: line
    };
    diffLines.push(diffLine);
    if (isFileDiffStubLine(line)) {
      rows.push({ hunkText: line, kind: "meta" });
    }
  });

  flushChangedRows();

  return { lines: diffLines, rows };
}

function parseHunkHeader(line: string) {
  const match = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
  if (!match) {
    return undefined;
  }

  return {
    newStart: Number.parseInt(match[2], 10),
    oldStart: Number.parseInt(match[1], 10)
  };
}

function updateSectionMetadata(section: DiffSectionDraft, line: string) {
  if (line.startsWith("diff --git ")) {
    const paths = parseDiffGitPaths(line);
    section.diffOldPath = paths?.oldPath;
    section.diffNewPath = paths?.newPath;
    return;
  }

  if (line.startsWith("--- ")) {
    section.oldPath = parseFileHeaderPath(line.slice(4));
    return;
  }

  if (line.startsWith("+++ ")) {
    section.newPath = parseFileHeaderPath(line.slice(4));
    return;
  }

  if (line.startsWith("new file mode ")) {
    section.newFile = true;
    return;
  }

  if (line.startsWith("deleted file mode ")) {
    section.deletedFile = true;
    return;
  }

  if (line.startsWith("rename from ")) {
    section.renameFrom = line.slice("rename from ".length).trim();
    return;
  }

  if (line.startsWith("rename to ")) {
    section.renameTo = line.slice("rename to ".length).trim();
  }
}

function parseDiffGitPaths(line: string) {
  const match = line.match(/^diff --git a\/(.+) b\/(.+)$/);
  if (!match) {
    return undefined;
  }

  return {
    newPath: match[2],
    oldPath: match[1]
  };
}

function parseFileHeaderPath(value: string) {
  const path = value.trim().split("\t")[0];
  if (!path || path === "/dev/null") {
    return undefined;
  }
  return path.replace(/^[ab]\//, "");
}

function changeKindForSection(
  section: DiffSectionDraft,
  oldPath: string | undefined,
  path: string
): FileChangeKind {
  if (section.newFile) {
    return "added";
  }
  if (section.deletedFile) {
    return "deleted";
  }
  if (section.renameFrom || section.renameTo || (oldPath && oldPath !== path)) {
    return "renamed";
  }
  return "modified";
}

function sectionHasBody(section: DiffSectionDraft) {
  return section.lines.some(
    (line) => line.startsWith("@@") || isAdditionLine(line) || isDeletionLine(line)
  );
}

function isAdditionLine(line: string) {
  return line.startsWith("+") && !line.startsWith("+++");
}

function isDeletionLine(line: string) {
  return line.startsWith("-") && !line.startsWith("---");
}

function isInsideHunk(oldLine: number, newLine: number) {
  return oldLine > 0 || newLine > 0;
}

function isFileDiffStubLine(line: string) {
  return (
    /^Binary files .+ differ$/.test(line) ||
    line.startsWith("Diff too large to render: ")
  );
}
