import { normalizeRepositoryPath } from "../sourceUtils";

export function joinRepositoryPath(directoryPath: string, name: string) {
  const cleanName = name.trim();
  const normalizedDirectory = normalizeDirectoryPath(directoryPath);
  return normalizeRepositoryPath(
    normalizedDirectory ? `${normalizedDirectory}/${cleanName}` : cleanName
  );
}

export function parentRepositoryPath(path: string) {
  const parts = normalizeRepositoryPath(path).split("/").filter(Boolean);
  if (parts.length <= 1) {
    return "";
  }
  return `/${parts.slice(0, -1).join("/")}`;
}

export function repositoryPathName(path: string) {
  const parts = normalizeRepositoryPath(path).split("/").filter(Boolean);
  return parts[parts.length - 1] ?? "";
}

export function validateEntryName(name: string) {
  const normalized = name.trim().replace(/\\/g, "/");
  if (!normalized) {
    return "Enter a name.";
  }
  const segments = normalized.split("/");
  for (const segment of segments) {
    if (segment === "") {
      return "Path can't have empty segments (no leading, trailing, or double slashes).";
    }
    if (segment !== segment.trim()) {
      return "Path segments can't start or end with a space.";
    }
    if (segment === "." || segment === "..") {
      return 'Path segments can\'t be "." or "..".';
    }
    // eslint-disable-next-line no-control-regex
    if (/[\u0000-\u001f\u007f]/.test(segment)) {
      return "Path can't contain control characters.";
    }
  }
  return "";
}

function normalizeDirectoryPath(path: string) {
  const normalized = normalizeRepositoryPath(path);
  return normalized === "/" ? "" : normalized;
}