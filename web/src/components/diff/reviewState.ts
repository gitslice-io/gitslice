import type { DiffFile } from "./parseDiff";

// What a reviewer keeps track of in a changeset: which files are generated
// (not worth reading line by line), which they have viewed, and whether a file
// has changed since they did.

// Files that are produced by a tool, by name: protobuf and Connect output,
// minified bundles, lock files and checksums, and anything called generated.
const generatedNames: RegExp[] = [
  /\.pb\.go$/,
  /\.pb\.gw\.go$/,
  /\.connect\.go$/,
  /_pb\.(?:ts|js|d\.ts)$/,
  /_pb2(?:_grpc)?\.pyi?$/,
  /\.min\.(?:js|css)$/,
  /(?:^|\/)(?:package-lock\.json|npm-shrinkwrap\.json|yarn\.lock|pnpm-lock\.yaml|go\.sum|Cargo\.lock|poetry\.lock|Gemfile\.lock|composer\.lock)$/,
  /(?:^|[._-])generated\.[a-z]+$/,
  /_generated\.[a-z]+$/
];

// The marker tools put at the top of what they write.
const generatedMarker = /code generated|do not edit|@generated/i;
const MARKER_LINES = 12;

export function isGeneratedPath(path: string): boolean {
  return generatedNames.some((pattern) => pattern.test(path));
}

export function isGeneratedFile(file: Pick<DiffFile, "path" | "lines">): boolean {
  if (isGeneratedPath(file.path)) {
    return true;
  }
  // The server sends whole files, so a generated file's header is among its
  // first lines whether or not it changed.
  return file.lines
    .filter((line) => line.kind === "context" || line.kind === "add" || line.kind === "del")
    .slice(0, MARKER_LINES)
    .some((line) => generatedMarker.test(line.content));
}

const fingerprints = new WeakMap<object, string>();

// A short hash of what a file's diff shows, so a file viewed in one patchset
// is unviewed in the next if it changed. "" for a file whose diff has not
// loaded (nothing to hash).
export function fileFingerprint(file: Pick<DiffFile, "lines">): string {
  if (file.lines.length === 0) {
    return "";
  }
  const cached = fingerprints.get(file.lines);
  if (cached !== undefined) {
    return cached;
  }
  let hash = 0x811c9dc5;
  for (const line of file.lines) {
    for (let i = 0; i < line.text.length; i++) {
      hash ^= line.text.charCodeAt(i);
      hash = Math.imul(hash, 0x01000193);
    }
    hash ^= 10;
    hash = Math.imul(hash, 0x01000193);
  }
  const result = (hash >>> 0).toString(36);
  fingerprints.set(file.lines, result);
  return result;
}

// Whether a file counts as viewed: the reviewer marked it, and it has not
// changed since. A file whose diff has not loaded yet keeps its mark until it
// does, and is checked then.
export function isViewed(file: Pick<DiffFile, "path" | "lines">, viewed: ViewedFiles): boolean {
  const mark = viewed[file.path];
  if (mark === undefined) {
    return false;
  }
  return file.lines.length === 0 || mark === fileFingerprint(file);
}

// Path to the fingerprint of the file as it was viewed.
export type ViewedFiles = Record<string, string>;

const STORAGE_KEY = "gitslice.viewedFiles.v1";
const MAX_SCOPES = 40;
const MAX_FILES = 1000;

interface Stored {
  [scope: string]: { at: number; files: ViewedFiles };
}

function read(storage: Pick<Storage, "getItem">): Stored {
  try {
    const parsed = JSON.parse(storage.getItem(STORAGE_KEY) ?? "{}");
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? (parsed as Stored) : {};
  } catch {
    return {};
  }
}

function defaultStorage(): Storage | undefined {
  try {
    return typeof window === "undefined" ? undefined : window.localStorage;
  } catch {
    return undefined;
  }
}

// What the reviewer has viewed in one changeset (the scope), on this browser.
export function loadViewed(scope: string, storage: Storage | undefined = defaultStorage()): ViewedFiles {
  if (!scope || !storage) {
    return {};
  }
  const files = read(storage)[scope]?.files;
  return files && typeof files === "object" ? { ...files } : {};
}

// Remember them. The most recently used scopes are kept, so the record does
// not grow with every changeset ever opened.
export function saveViewed(
  scope: string,
  files: ViewedFiles,
  storage: Storage | undefined = defaultStorage(),
  now: number = Date.now()
): void {
  if (!scope || !storage) {
    return;
  }
  try {
    const stored = read(storage);
    const entries = Object.entries(files).slice(-MAX_FILES);
    if (entries.length === 0) {
      delete stored[scope];
    } else {
      stored[scope] = { at: now, files: Object.fromEntries(entries) };
    }
    const kept = Object.entries(stored)
      .sort((a, b) => b[1].at - a[1].at)
      .slice(0, MAX_SCOPES);
    storage.setItem(STORAGE_KEY, JSON.stringify(Object.fromEntries(kept)));
  } catch {
    // Storage full or blocked: the marks last until the page closes.
  }
}
