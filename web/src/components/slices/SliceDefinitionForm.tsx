import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";

import type { TreeEntry } from "../../api/types";
import { useApi } from "../../api/useApi";
import { GLOBAL_REF_NAME } from "../../lib/globalRef";
import { SlicePanel } from "./SlicePageParts";

export const VISIBILITY_OPTIONS = ["private", "public"] as const;

export type VisibilityOption = (typeof VISIBILITY_OPTIONS)[number];

interface SliceDefinitionFormProps {
  account?: string;
  visibility: VisibilityOption;
  onVisibilityChange(v: VisibilityOption): void;
  includedPaths: string[];
  onIncludedPathsChange(paths: string[]): void;
  disabled?: boolean;
  includedPathsLocked?: boolean;
}

export function SliceDefinitionForm({
  account,
  visibility,
  onVisibilityChange,
  includedPaths,
  onIncludedPathsChange,
  disabled = false,
  includedPathsLocked = false
}: SliceDefinitionFormProps) {
  const api = useApi();
  const cleanAccount = account?.trim().replace(/^\/+|\/+$/g, "") ?? "";
  const defaultPathDraft = cleanAccount ? `/${cleanAccount}/` : "/";
  const previousDefaultPathDraft = useRef(defaultPathDraft);
  const pathDraftInput = useRef<HTMLInputElement>(null);
  const [pathDraft, setPathDraft] = useState(defaultPathDraft);
  const [isPathDraftFocused, setIsPathDraftFocused] = useState(false);
  const trimmedPathDraft = pathDraft.trim();

  useEffect(() => {
    setPathDraft((currentDraft) => {
      if (
        currentDraft === previousDefaultPathDraft.current ||
        currentDraft.trim() === "" ||
        currentDraft === "/"
      ) {
        return defaultPathDraft;
      }

      return currentDraft;
    });
    previousDefaultPathDraft.current = defaultPathDraft;
  }, [defaultPathDraft]);

  const latestGlobalRefQuery = useQuery({
    queryKey: ["globalRef", GLOBAL_REF_NAME],
    queryFn: () => api.getRef({ refName: GLOBAL_REF_NAME })
  });
  const commitId = latestGlobalRefQuery.data?.commitId ?? "";
  const lastSlash = pathDraft.lastIndexOf("/");
  const partial = lastSlash >= 0 ? pathDraft.slice(lastSlash + 1) : pathDraft;
  const draftDirPath = lastSlash >= 0 ? pathDraft.slice(0, lastSlash) : "";
  const dirPath = draftDirPath === "" ? "/" : draftDirPath;

  const pathSuggestionsQuery = useQuery({
    enabled: Boolean(commitId),
    queryKey: ["pathSuggest", commitId, dirPath],
    queryFn: async () => {
      try {
        return await api.listDirectory({
          commitId,
          pageSize: 200,
          path: dirPath
        });
      } catch {
        return { entries: [] };
      }
    }
  });

  const pathSuggestions = useMemo(() => {
    const partialLower = partial.toLowerCase();

    return (pathSuggestionsQuery.data?.entries ?? [])
      .filter((entry): entry is TreeEntry & { name: string } => {
        if (!entry.name) {
          return false;
        }

        return (
          partialLower === "" ||
          entry.name.toLowerCase().startsWith(partialLower)
        );
      })
      .sort((left, right) => {
        const leftIsDirectory = left.kind === "ENTRY_KIND_DIRECTORY";
        const rightIsDirectory = right.kind === "ENTRY_KIND_DIRECTORY";

        if (leftIsDirectory !== rightIsDirectory) {
          return leftIsDirectory ? -1 : 1;
        }

        return left.name.localeCompare(right.name);
      })
      .slice(0, 8);
  }, [partial, pathSuggestionsQuery.data?.entries]);

  const showPathSuggestions =
    !disabled && isPathDraftFocused && pathSuggestions.length > 0;
  const canAddPath = !disabled && trimmedPathDraft !== "" && trimmedPathDraft !== "/";

  function addPath() {
    if (disabled) {
      return;
    }

    const nextPath = trimmedPathDraft;

    if (!nextPath || nextPath === "/") {
      return;
    }

    onIncludedPathsChange([...includedPaths, nextPath]);
    setPathDraft(defaultPathDraft);
  }

  function selectPathSuggestion(entry: TreeEntry & { name: string }) {
    const basePath = dirPath === "/" ? "" : dirPath;
    const suffix = entry.kind === "ENTRY_KIND_DIRECTORY" ? "/" : "";

    setPathDraft(`${basePath}/${entry.name}${suffix}`);
    pathDraftInput.current?.focus();
  }

  function updatePath(index: number, value: string) {
    onIncludedPathsChange(
      includedPaths.map((path, pathIndex) =>
        pathIndex === index ? value : path
      )
    );
  }

  function removePath(index: number) {
    onIncludedPathsChange(
      includedPaths.filter((_, pathIndex) => pathIndex !== index)
    );
  }

  return (
    <>
      <SlicePanel className="space-y-4">
        <div>
          <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
            Visibility
          </h2>
          <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            Choose one of the visibility values supported by the slice
            definition.
          </p>
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          {VISIBILITY_OPTIONS.map((option) => (
            <label
              className="flex items-center gap-3 rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2 text-sm font-medium text-zinc-950 dark:text-zinc-50"
              key={option}
            >
              <input
                checked={visibility === option}
                className="h-4 w-4 accent-zinc-950 disabled:cursor-not-allowed"
                disabled={disabled}
                name="visibility"
                onChange={() => onVisibilityChange(option)}
                type="radio"
                value={option}
              />
              {option}
            </label>
          ))}
        </div>
      </SlicePanel>

      <SlicePanel className="space-y-4">
        <div>
          <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
            Included paths
          </h2>
          <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
            {includedPathsLocked
              ? "The home slice's included paths are managed automatically and can't be changed."
              : "Paths should be account-rooted, for example /acme/payment. The server performs final validation."}
          </p>
        </div>

        {includedPathsLocked ? (
          <ul className="space-y-2">
            {includedPaths.map((path, index) => (
              <li
                className="break-all rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2 font-mono text-sm text-slate-700 dark:text-zinc-300"
                key={index}
              >
                {path}
              </li>
            ))}
          </ul>
        ) : (
          <>
        <div className="space-y-3">
          {includedPaths.length === 0 ? (
            <div className="rounded-md border border-dashed border-slate-300 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-950 p-4 text-sm text-slate-600 dark:text-zinc-400">
              No included paths are currently set.
            </div>
          ) : (
            includedPaths.map((path, index) => (
              <div
                className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]"
                key={index}
              >
                <label className="grid gap-2 text-sm font-medium text-zinc-950 dark:text-zinc-50">
                  Path {index + 1}
                  <input
                    className="h-10 min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 font-mono text-sm text-zinc-950 dark:text-zinc-50 outline-none transition focus:border-slate-500 dark:focus:border-zinc-500 disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-500 dark:disabled:text-zinc-400"
                    disabled={disabled}
                    onChange={(event) =>
                      updatePath(index, event.target.value)
                    }
                    placeholder="/acme/payment"
                    spellCheck={false}
                    value={path}
                  />
                </label>
                <button
                  className="self-end rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-400 dark:disabled:text-zinc-500"
                  disabled={disabled}
                  onClick={() => removePath(index)}
                  type="button"
                >
                  Remove
                </button>
              </div>
            ))
          )}
        </div>

        <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
          <label className="grid gap-2 text-sm font-medium text-zinc-950 dark:text-zinc-50">
            Add path
            <div className="relative">
              <input
                aria-expanded={showPathSuggestions}
                aria-haspopup="listbox"
                className="h-10 w-full min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 font-mono text-sm text-zinc-950 dark:text-zinc-50 outline-none transition focus:border-slate-500 dark:focus:border-zinc-500 disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-500 dark:disabled:text-zinc-400"
                disabled={disabled}
                onBlur={() => setIsPathDraftFocused(false)}
                onChange={(event) => setPathDraft(event.target.value)}
                onFocus={() => setIsPathDraftFocused(true)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    addPath();
                  }
                }}
                placeholder="/acme/proto/payment"
                ref={pathDraftInput}
                spellCheck={false}
                value={pathDraft}
              />
              {showPathSuggestions ? (
                <div
                  className="absolute left-0 right-0 top-full z-20 mt-1 overflow-hidden rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-lg"
                  role="listbox"
                >
                  {pathSuggestions.map((entry) => (
                    <button
                      className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left font-mono text-sm text-zinc-950 dark:text-zinc-50 transition hover:bg-slate-50 dark:hover:bg-zinc-950 focus:bg-slate-50 dark:focus:bg-zinc-950 focus:outline-none"
                      key={`${entry.kind ?? "entry"}:${entry.name}`}
                      onClick={() => selectPathSuggestion(entry)}
                      onMouseDown={(event) => event.preventDefault()}
                      role="option"
                      type="button"
                    >
                      <span className="min-w-0 truncate">{entry.name}</span>
                      <span className="shrink-0 rounded border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 text-[11px] font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">
                        {entry.kind === "ENTRY_KIND_DIRECTORY" ? "dir" : "file"}
                      </span>
                    </button>
                  ))}
                </div>
              ) : null}
            </div>
          </label>
          <button
            className="self-end rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-4 py-2 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-400 dark:disabled:text-zinc-500"
            disabled={!canAddPath}
            onClick={addPath}
            type="button"
          >
            Add path
          </button>
        </div>
          </>
        )}
      </SlicePanel>
    </>
  );
}

export function toVisibilityOption(value?: string): VisibilityOption {
  if (value === "private" || value === "public") {
    return value;
  }

  return "private";
}

export function validateIncludedPaths(paths: string[]) {
  const errors: string[] = [];
  const normalizedPaths = paths.map((path) => path.trim()).filter(Boolean);
  const seen = new Set<string>();

  if (paths.some((path) => !path.trim())) {
    errors.push("Included paths cannot be blank.");
  }

  if (normalizedPaths.length === 0) {
    errors.push("At least one included path is required.");
  }

  normalizedPaths.forEach((path) => {
    if (!path.startsWith("/")) {
      errors.push(`${path} must start with /.`);
    }

    if (path === "/") {
      errors.push("/ must include an account segment.");
    }

    if (path.length > 1 && path.endsWith("/")) {
      errors.push(`${path} must not end with /.`);
    }

    if (path.includes("//")) {
      errors.push(`${path} must not contain empty path segments.`);
    }

    if (path.includes("\0")) {
      errors.push(`${path} contains an invalid null character.`);
    }

    const segments = path.split("/").filter(Boolean);
    if (segments.some((segment) => segment === "." || segment === "..")) {
      errors.push(`${path} must not contain . or .. path segments.`);
    }

    if (seen.has(path)) {
      errors.push(`${path} is duplicated.`);
    }

    seen.add(path);
  });

  return {
    errors: Array.from(new Set(errors)),
    paths: normalizedPaths
  };
}
