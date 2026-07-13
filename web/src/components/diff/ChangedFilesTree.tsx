import { useMemo, useState } from "react";

import { cn } from "../../lib/cn";
import { FileTypeIcon } from "../source/FileTypeIcon";
import type { DiffFile, FileChangeKind } from "./parseDiff";

interface ChangedFilesTreeProps {
  activeId?: string;
  files: DiffFile[];
  onSelect(id: string): void;
  scrollClassName?: string;
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
  files,
  onSelect,
  scrollClassName
}: ChangedFilesTreeProps) {
  const tree = useMemo(() => buildTree(files), [files]);
  const [collapsedPaths, setCollapsedPaths] = useState<Set<string>>(
    () => new Set()
  );

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

  if (files.length === 0) {
    return (
      <div className="rounded-lg border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-4 text-sm text-slate-500 dark:text-zinc-400">
        No changed files.
      </div>
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900">
      <div className="border-b border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
        Files
      </div>
      <div
        className={cn(
          "max-h-[68dvh] overflow-auto py-1 font-mono text-xs lg:max-h-[calc(100dvh-15rem)]",
          scrollClassName
        )}
      >
        {tree.map((node) => (
          <TreeNodeRow
            activeId={activeId}
            collapsedPaths={collapsedPaths}
            depth={0}
            key={node.path}
            node={node}
            onSelect={onSelect}
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
        className="flex w-full items-center gap-1 px-2 py-1 text-left text-slate-600 dark:text-zinc-400 transition hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50"
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
          : "text-slate-700 dark:text-zinc-300 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50"
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
          active ? "text-slate-200" : "text-slate-500 dark:text-zinc-400"
        )}
      >
        <span className={active ? "text-emerald-200" : "text-emerald-700 dark:text-emerald-300"}>
          +{file.additions}
        </span>
        <span className={active ? "text-rose-200" : "text-rose-700 dark:text-rose-300"}>
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
    case "pending":
      return "·";
  }
}

function statusGlyphClass(kind: FileChangeKind) {
  switch (kind) {
    case "added":
      return "bg-emerald-100 dark:bg-emerald-950/45 text-emerald-800 dark:text-emerald-200";
    case "deleted":
      return "bg-rose-100 dark:bg-rose-950/45 text-rose-800 dark:text-rose-200";
    case "renamed":
      return "bg-violet-100 text-violet-800";
    case "modified":
      return "bg-amber-100 dark:bg-amber-950/45 text-amber-900 dark:text-amber-200";
    case "pending":
      return "bg-slate-100 dark:bg-zinc-800 text-slate-500 dark:text-zinc-400";
  }
}
