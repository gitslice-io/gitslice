import { useState } from "react";

import { SlicePanel } from "./SlicePageParts";

export const VISIBILITY_OPTIONS = ["private", "account", "public"] as const;

export type VisibilityOption = (typeof VISIBILITY_OPTIONS)[number];

interface SliceDefinitionFormProps {
  visibility: VisibilityOption;
  onVisibilityChange(v: VisibilityOption): void;
  includedPaths: string[];
  onIncludedPathsChange(paths: string[]): void;
  disabled?: boolean;
}

export function SliceDefinitionForm({
  visibility,
  onVisibilityChange,
  includedPaths,
  onIncludedPathsChange,
  disabled = false
}: SliceDefinitionFormProps) {
  const [pathDraft, setPathDraft] = useState("");

  function addPath() {
    if (disabled) {
      return;
    }

    const nextPath = pathDraft.trim();

    if (!nextPath) {
      return;
    }

    onIncludedPathsChange([...includedPaths, nextPath]);
    setPathDraft("");
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
          <h2 className="text-base font-semibold text-zinc-950">
            Visibility
          </h2>
          <p className="mt-1 text-sm leading-6 text-slate-600">
            Choose one of the visibility values supported by the slice
            definition.
          </p>
        </div>
        <div className="grid gap-3 sm:grid-cols-3">
          {VISIBILITY_OPTIONS.map((option) => (
            <label
              className="flex items-center gap-3 rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-sm font-medium text-zinc-950"
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
          <h2 className="text-base font-semibold text-zinc-950">
            Included paths
          </h2>
          <p className="mt-1 text-sm leading-6 text-slate-600">
            Paths should be account-rooted, for example /acme/payment. The
            server performs final validation.
          </p>
        </div>

        <div className="space-y-3">
          {includedPaths.length === 0 ? (
            <div className="rounded-md border border-dashed border-slate-300 bg-slate-50 p-4 text-sm text-slate-600">
              No included paths are currently set.
            </div>
          ) : (
            includedPaths.map((path, index) => (
              <div
                className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]"
                key={index}
              >
                <label className="grid gap-2 text-sm font-medium text-zinc-950">
                  Path {index + 1}
                  <input
                    className="h-10 min-w-0 rounded-md border border-slate-300 bg-white px-3 font-mono text-sm text-zinc-950 outline-none transition focus:border-slate-500 disabled:cursor-not-allowed disabled:bg-slate-100 disabled:text-slate-500"
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
                  className="self-end rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-100 disabled:text-slate-400"
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
          <label className="grid gap-2 text-sm font-medium text-zinc-950">
            Add path
            <input
              className="h-10 min-w-0 rounded-md border border-slate-300 bg-white px-3 font-mono text-sm text-zinc-950 outline-none transition focus:border-slate-500 disabled:cursor-not-allowed disabled:bg-slate-100 disabled:text-slate-500"
              disabled={disabled}
              onChange={(event) => setPathDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  addPath();
                }
              }}
              placeholder="/acme/proto/payment"
              spellCheck={false}
              value={pathDraft}
            />
          </label>
          <button
            className="self-end rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-100 disabled:text-slate-400"
            disabled={disabled || !pathDraft.trim()}
            onClick={addPath}
            type="button"
          >
            Add path
          </button>
        </div>
      </SlicePanel>
    </>
  );
}

export function toVisibilityOption(value?: string): VisibilityOption {
  if (value === "private" || value === "public" || value === "account") {
    return value;
  }

  return "account";
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
