import { useMemo, useState } from "react";

import { cn } from "../../lib/cn";
import { FileTypeIcon } from "../source/FileTypeIcon";
import type { DiffFile, FileChangeKind } from "./parseDiff";

interface ChangedFilesTreeProps {
  activeId?: string;
  changedCount?: number;
  files: DiffFile[];
  mobileCompact?: boolean;
  onSelect(id: string): void;
  totalAdditions?: number;
  totalDeletions?: number;
}

interface DirectoryNode {
  children: TreeNode[];
  name: string;
  path: string;
  type: "directory";
}

interface FileNode {
  file: DiffFile;
  name: string;
  path: string;
  type: "file";
}

type TreeNode = DirectoryNode | FileNode;

interface BuildDirectory {
  directories: Map<string, BuildDirectory>;
  files: FileNode[];
  name: string;
  path: string;
}

export function ChangedFilesTree({
  activeId,
  changedCount,
  files,
  mobileCompact,
  onSelect,
  totalAdditions,
  totalDeletions
}: ChangedFilesTreeProps) {
  const tree = useMemo(() => buildTree(files), [files]);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [collapsedPaths, setCollapsedPaths] = useState<Set<string>>(
    () => new Set()
  );
  const summaryChangedCount = changedCount ?? files.length;
  const summaryAdditions =
    totalAdditions ?? files.reduce((total, file) => total + file.additions, 0);
  const summaryDeletions =
    totalDeletions ?? files.reduce((total, file) => total + file.deletions, 0);

  const toggleDirectory = (path: string) => {
    setCollapsedPaths((current) => {
      const next = new Set(current);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  };

  const selectFile = (id: string) => {
    onSelect(id);
    if (mobileCompact) {
      setMobileOpen(false);
    }
  };

  if (files.length === 0) {
    return (
      <div className="rounded-lg border border-slate-200 bg-slate-50 px-3 py-4 text-sm text-slate-500">
        No changed files.
      </div>
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
      {mobileCompact ? (
        <button
          aria-expanded={mobileOpen}
          className="grid w-full grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-2 bg-slate-50 px-3 py-2.5 text-left transition hover:bg-slate-100 lg:hidden"
          onClick={() => setMobileOpen((value) => !value)}
          type="button"
        >
          <span className="min-w-0 truncate text-sm font-semibold text-zinc-950">
            {summaryChangedCount} {summaryChangedCount === 1 ? "file" : "files"}{" "}
            changed
          </span>
          <span className="flex shrink-0 gap-1.5 font-mono text-xs">
            <span className="text-emerald-700">+{summaryAdditions}</span>
            <span className="text-slate-300">/</span>
            <span className="text-rose-700">-{summaryDeletions}</span>
          </span>
          <span
            aria-hidden="true"
            className={cn(
              "inline-block w-3 shrink-0 text-[10px] text-slate-400 transition-transform",
              mobileOpen && "rotate-90"
            )}
          >
            &gt;
          </span>
        </button>
      ) : null}
      <div
        className={cn(
          "border-b border-slate-200 bg-slate-50 px-3 py-2 text-xs font-semibold uppercase tracking-normal text-slate-500",
          mobileCompact && "hidden lg:block"
        )}
      >
        Files
      </div>
      <div
        className={cn(
          "overflow-auto py-1 font-mono text-xs",
          mobileCompact && "border-t border-slate-200 lg:border-t-0",
          mobileCompact && (mobileOpen ? "block" : "hidden lg:block"),
          mobileCompact
            ? "max-h-56 lg:max-h-[calc(100dvh-15rem)]"
            : "max-h-[68dvh] lg:max-h-[calc(100dvh-15rem)]"
        )}
      >
        {tree.map((node) => (
          <TreeNodeRow
            activeId={activeId}
            collapsedPaths={collapsedPaths}
            depth={0}
            key={node.path}
            node={node}
            onSelect={selectFile}
            onToggleDirectory={toggleDirectory}
          />
        ))}
      </div>
    </div>
  );
}

function TreeNodeRow({
  activeId,
  collapsedPaths,
  depth,
  node,
  onSelect,
  onToggleDirectory
}: {
  activeId?: string;
  collapsedPaths: Set<string>;
  depth: number;
  node: TreeNode;
  onSelect(id: string): void;
  onToggleDirectory(path: string): void;
}) {
  if (node.type === "file") {
    return (
      <FileRow
        active={node.file.id === activeId}
        depth={depth}
        file={node.file}
        name={node.name}
        onSelect={onSelect}
      />
    );
  }

  const collapsed = collapseDirectoryChain(node);
  const isCollapsed = collapsedPaths.has(collapsed.path);

  return (
    <div>
      <button
        aria-expanded={!isCollapsed}
        className="flex w-full items-center gap-1 px-2 py-1 text-left text-slate-600 transition hover:bg-slate-50 hover:text-zinc-950"
        onClick={() => onToggleDirectory(collapsed.path)}
        style={{ paddingLeft: `${depth * 0.75 + 0.5}rem` }}
        type="button"
      >
        <span
          aria-hidden="true"
          className={cn(
            "inline-block w-3 shrink-0 text-[10px] transition-transform",
            !isCollapsed && "rotate-90"
          )}
        >
          &gt;
        </span>
        <span className="truncate">{collapsed.name}</span>
      </button>
      {!isCollapsed
        ? collapsed.children.map((child) => (
            <TreeNodeRow
              activeId={activeId}
              collapsedPaths={collapsedPaths}
              depth={depth + 1}
              key={child.path}
              node={child}
              onSelect={onSelect}
              onToggleDirectory={onToggleDirectory}
            />
          ))
        : null}
    </div>
  );
}

function FileRow({
  active,
  depth,
  file,
  name,
  onSelect
}: {
  active: boolean;
  depth: number;
  file: DiffFile;
  name: string;
  onSelect(id: string): void;
}) {
  return (
    <button
      aria-current={active ? "true" : undefined}
      className={cn(
        "grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 px-2 py-1 text-left transition",
        active
          ? "bg-slate-900 text-white"
          : "text-slate-700 hover:bg-slate-50 hover:text-zinc-950"
      )}
      onClick={() => onSelect(file.id)}
      style={{ paddingLeft: `${depth * 0.75 + 1.25}rem` }}
      type="button"
    >
      <span
        className={cn(
          "inline-flex h-4 w-4 shrink-0 items-center justify-center rounded text-[10px] font-bold",
          statusGlyphClass(file.changeKind)
        )}
      >
        {statusGlyph(file.changeKind)}
      </span>
      <span className="flex min-w-0 items-center gap-1.5" title={file.path}>
        <FileTypeIcon name={name} />
        <span className="min-w-0 truncate">{name}</span>
      </span>
      <span
        className={cn(
          "flex shrink-0 gap-1 text-[11px]",
          active ? "text-slate-200" : "text-slate-500"
        )}
      >
        <span className={active ? "text-emerald-200" : "text-emerald-700"}>
          +{file.additions}
        </span>
        <span className={active ? "text-rose-200" : "text-rose-700"}>
          -{file.deletions}
        </span>
      </span>
    </button>
  );
}

function buildTree(files: DiffFile[]): TreeNode[] {
  const root: BuildDirectory = {
    directories: new Map(),
    files: [],
    name: "",
    path: ""
  };

  files.forEach((file) => {
    const parts = file.path.split("/").filter(Boolean);
    const fileName = parts.pop() || file.path || "unknown";
    let directory = root;

    parts.forEach((part) => {
      const path = directory.path ? `${directory.path}/${part}` : part;
      const existing = directory.directories.get(part);

      if (existing) {
        directory = existing;
        return;
      }

      const next: BuildDirectory = {
        directories: new Map(),
        files: [],
        name: part,
        path
      };
      directory.directories.set(part, next);
      directory = next;
    });

    directory.files.push({
      file,
      name: fileName,
      path: file.path,
      type: "file"
    });
  });

  return childrenForDirectory(root);
}

function childrenForDirectory(directory: BuildDirectory): TreeNode[] {
  const directories = Array.from(directory.directories.values())
    .sort((left, right) => left.name.localeCompare(right.name))
    .map<DirectoryNode>((child) => ({
      children: childrenForDirectory(child),
      name: child.name,
      path: child.path,
      type: "directory"
    }));

  const files = directory.files.sort((left, right) =>
    left.name.localeCompare(right.name)
  );

  return [...directories, ...files];
}

function collapseDirectoryChain(node: DirectoryNode): DirectoryNode {
  let name = node.name;
  let path = node.path;
  let children = node.children;

  while (children.length === 1 && children[0].type === "directory") {
    const child = children[0];
    name = `${name}/${child.name}`;
    path = child.path;
    children = child.children;
  }

  return {
    children,
    name,
    path,
    type: "directory"
  };
}

function statusGlyph(kind: FileChangeKind) {
  switch (kind) {
    case "added":
      return "A";
    case "deleted":
      return "D";
    case "renamed":
      return "R";
    case "modified":
      return "M";
  }
}

function statusGlyphClass(kind: FileChangeKind) {
  switch (kind) {
    case "added":
      return "bg-emerald-100 text-emerald-800";
    case "deleted":
      return "bg-rose-100 text-rose-800";
    case "renamed":
      return "bg-violet-100 text-violet-800";
    case "modified":
      return "bg-amber-100 text-amber-900";
  }
}
