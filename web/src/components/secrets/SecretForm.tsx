import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import type { Secret, SecretScope } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { getErrorMessage } from "../slices/SlicePageParts";

// Secrets (design/33_secrets.md): values passed, as environment variables, to
// the CI checks defined in the folders each one applies to and below them.

export const SECRET_NAME_PATTERN = /^[A-Z_][A-Z0-9_]*$/;
export const MAX_SECRET_VALUE_BYTES = 48 * 1024;
export const MAX_SECRET_PATHS = 50;

export const secretButtonClass =
  "rounded-md border border-slate-200 px-2.5 py-1 text-xs font-medium text-slate-700 transition hover:bg-slate-50 disabled:opacity-50 dark:border-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-950";
export const secretPrimaryButtonClass =
  "rounded-md bg-zinc-950 px-3 py-1.5 text-sm font-semibold text-white transition hover:bg-zinc-800 disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white";
export const secretDangerButtonClass = cn(
  secretButtonClass,
  "text-rose-700 hover:bg-rose-50 dark:text-rose-300 dark:hover:bg-rose-950/30"
);
const inputClass =
  "h-9 min-w-0 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:bg-slate-50 disabled:text-slate-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:focus:ring-zinc-700 dark:disabled:bg-zinc-950";

export interface SecretDraft {
  name: string;
  // "" when editing means: keep the current value.
  value: string;
  // Absolute folders, such as /acme/services.
  paths: string[];
}

// secretPaths reads a scope's folders. A server older than folders sends
// allSlices (the account root) or slices, which have no folder to show.
export function secretPaths(account: string, scope: SecretScope | undefined): string[] {
  if ((scope?.paths ?? []).length > 0) return scope?.paths ?? [];
  return scope?.allSlices ? [`/${account}`] : [];
}

export function draftFromSecret(account: string, secret: Secret): SecretDraft {
  return { name: secret.name ?? "", paths: secretPaths(account, secret.scope), value: "" };
}

export function scopeFromDraft(draft: SecretDraft): SecretScope {
  return { paths: draft.paths };
}

// scopeLabel names the folders a secret applies to.
export function scopeLabel(account: string, scope: SecretScope | undefined) {
  const paths = secretPaths(account, scope);
  if (paths.length > 0) return paths.join(", ");
  const slices = (scope?.slices ?? []).map((ref) => ref.slice ?? "").filter(Boolean);
  return slices.length > 0 ? `Slices ${slices.join(", ")}` : "No folders";
}

// normalizeSecretPath cleans a typed folder the way the server will: one
// leading slash, no trailing or doubled slashes. "" when it is not a folder
// of the account.
export function normalizeSecretPath(account: string, raw: string): string {
  const parts = raw.trim().split("/").filter(Boolean);
  if (parts.length === 0 || parts[0] !== account || parts.some((part) => part === "." || part === "..")) return "";
  if (!raw.trim().startsWith("/")) return "";
  return `/${parts.join("/")}`;
}

export const SECRET_EXPOSURE_NOTE =
  "Anyone who can add a check in these folders can read the secret from it: writers of slices that include a folder, one above it or one inside it. Those slices' CI daemons get it too.";

interface FolderChoice {
  label: string;
  paths: string[];
}

// SecretForm creates a secret or edits one. A secret's name never changes,
// and its value is never shown: editing leaves it as it is unless a new one
// is typed.
export function SecretForm({
  account,
  busy,
  editing = false,
  initial,
  onCancel,
  onSubmit,
  problem: serverProblem,
  submitLabel
}: {
  account: string;
  busy: boolean;
  editing?: boolean;
  initial: SecretDraft;
  onCancel(): void;
  onSubmit(draft: SecretDraft): void;
  problem?: string;
  submitLabel: string;
}) {
  const api = useApi();
  const [draft, setDraft] = useState<SecretDraft>(initial);
  const [problem, setProblem] = useState("");
  const [showValue, setShowValue] = useState(false);
  const [typed, setTyped] = useState("");

  // Shortcuts: the whole account, and the folders of each of its slices.
  const slicesQuery = useQuery({
    queryKey: ["secretScopeSlices", account],
    queryFn: async () =>
      ((await api.listSlices({ account, pageSize: 200 })).slices ?? [])
        .map((slice) => ({ label: slice.ref?.slice ?? "", paths: slice.definition?.includedPaths ?? [] }))
        .filter((choice) => choice.label !== "" && choice.paths.length > 0)
  });
  const choices: FolderChoice[] = [{ label: `All of ${account}`, paths: [`/${account}`] }, ...(slicesQuery.data ?? [])];

  function addPaths(paths: string[]) {
    setDraft((current) => ({
      ...current,
      paths: Array.from(new Set([...current.paths, ...paths])).sort()
    }));
  }

  function removePath(path: string) {
    setDraft((current) => ({ ...current, paths: current.paths.filter((p) => p !== path) }));
  }

  function addTyped() {
    if (typed.trim() === "") return;
    const path = normalizeSecretPath(account, typed);
    if (path === "") {
      setProblem(`A folder starts with /${account}, such as /${account}/services.`);
      return;
    }
    setProblem("");
    addPaths([path]);
    setTyped("");
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const name = draft.name.trim();
    if (!SECRET_NAME_PATTERN.test(name)) {
      setProblem("Use an environment variable name: capital letters, digits and _, not starting with a digit.");
      return;
    }
    if (!editing && draft.value === "") {
      setProblem("Enter the secret's value.");
      return;
    }
    if (new TextEncoder().encode(draft.value).length > MAX_SECRET_VALUE_BYTES) {
      setProblem("A secret value can be at most 48 KiB.");
      return;
    }
    if (draft.paths.length === 0) {
      setProblem("Add at least one folder.");
      return;
    }
    if (draft.paths.length > MAX_SECRET_PATHS) {
      setProblem(`A secret can apply to at most ${MAX_SECRET_PATHS} folders.`);
      return;
    }
    setProblem("");
    onSubmit({ ...draft, name });
  }

  return (
    <form
      aria-label={submitLabel}
      className="mt-4 grid gap-4 rounded-lg border border-slate-200 p-4 dark:border-zinc-800"
      onSubmit={submit}
    >
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Name
        <input
          autoCapitalize="characters"
          autoComplete="off"
          className={cn(inputClass, "font-mono")}
          disabled={busy || editing}
          onChange={(event) => setDraft({ ...draft, name: event.target.value.toUpperCase() })}
          placeholder="DEPLOY_TOKEN"
          spellCheck={false}
          value={draft.name}
        />
        <span className="font-normal text-slate-500 dark:text-zinc-400">
          {editing ? "A secret's name cannot change." : "Checks read it as this environment variable."}
        </span>
      </label>

      <div className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        <div className="flex items-center justify-between gap-2">
          <label htmlFor="secret-value">Value</label>
          <button
            className="text-xs font-normal text-slate-500 underline-offset-2 hover:underline dark:text-zinc-400"
            onClick={() => setShowValue(!showValue)}
            type="button"
          >
            {showValue ? "Hide" : "Show"}
          </button>
        </div>
        <textarea
          autoComplete="off"
          className={cn(
            "min-h-[4.5rem] min-w-0 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:focus:ring-zinc-700",
            !showValue && "[-webkit-text-security:disc]"
          )}
          data-1p-ignore
          disabled={busy}
          id="secret-value"
          onChange={(event) => setDraft({ ...draft, value: event.target.value })}
          placeholder={editing ? "unchanged" : ""}
          rows={3}
          spellCheck={false}
          value={draft.value}
        />
        <span className="font-normal text-slate-500 dark:text-zinc-400">
          {editing
            ? "Leave empty to keep the current value. Values are never shown once saved."
            : "Values are never shown once saved."}
        </span>
      </div>

      <fieldset className="grid gap-2">
        <legend className="mb-1 text-xs font-medium text-slate-600 dark:text-zinc-400">Folders</legend>
        <p className="text-xs leading-5 text-slate-500 dark:text-zinc-400">
          Checks defined in these folders, or below them, get the secret. Where two secrets of one name apply, the one
          on the closer folder wins.
        </p>
        {draft.paths.length === 0 ? (
          <p className="text-xs text-slate-500 dark:text-zinc-400">No folders yet.</p>
        ) : (
          <ul aria-label="Folders" className="flex flex-wrap gap-1.5">
            {draft.paths.map((path) => (
              <li
                className="flex min-w-0 max-w-full items-center gap-1 rounded-md border border-slate-200 bg-slate-50 py-0.5 pl-2 pr-1 dark:border-zinc-800 dark:bg-zinc-950"
                key={path}
              >
                <code className="truncate font-mono text-xs text-zinc-900 dark:text-zinc-100">{path}</code>
                <button
                  aria-label={`Remove ${path}`}
                  className="rounded px-1.5 py-0.5 text-xs text-slate-500 hover:bg-slate-200 hover:text-zinc-950 disabled:opacity-50 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-50"
                  disabled={busy}
                  onClick={() => removePath(path)}
                  type="button"
                >
                  ×
                </button>
              </li>
            ))}
          </ul>
        )}
        <div className="flex min-w-0 gap-2">
          <input
            aria-label="Add a folder"
            autoComplete="off"
            className={cn(inputClass, "flex-1 font-mono text-xs")}
            disabled={busy}
            onChange={(event) => setTyped(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                addTyped();
              }
            }}
            placeholder={`/${account}/services`}
            spellCheck={false}
            value={typed}
          />
          <button className={secretButtonClass} disabled={busy || typed.trim() === ""} onClick={addTyped} type="button">
            Add folder
          </button>
        </div>
        <div className="flex flex-wrap items-center gap-1.5 text-xs text-slate-500 dark:text-zinc-400">
          <span>Add:</span>
          {(choices.length > 9 ? choices.slice(0, 1) : choices).map((choice) => (
            <button
              className={cn(secretButtonClass, "py-0.5")}
              disabled={busy || choice.paths.every((path) => draft.paths.includes(path))}
              key={choice.label}
              onClick={() => addPaths(choice.paths)}
              title={choice.paths.join(", ")}
              type="button"
            >
              {choice.label}
            </button>
          ))}
          {choices.length > 9 ? (
            // Many slices: a list instead of a row of buttons.
            <select
              aria-label="Add a slice's folders"
              className={cn(inputClass, "h-7 text-xs")}
              disabled={busy}
              onChange={(event) => {
                const choice = choices.find((c) => c.label === event.target.value);
                if (choice) addPaths(choice.paths);
                event.target.value = "";
              }}
              value=""
            >
              <option value="">A slice's folders...</option>
              {choices.slice(1).map((choice) => (
                <option key={choice.label} value={choice.label}>
                  {choice.label}
                </option>
              ))}
            </select>
          ) : null}
          {slicesQuery.isError ? (
            <span className="text-rose-700 dark:text-rose-300">{getErrorMessage(slicesQuery.error)}</span>
          ) : null}
        </div>
        <p className="text-xs leading-5 text-amber-800 dark:text-amber-300">{SECRET_EXPOSURE_NOTE}</p>
      </fieldset>

      {problem || serverProblem ? (
        <p className="text-sm text-rose-700 dark:text-rose-300" role="alert">
          {problem || serverProblem}
        </p>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <button className={secretPrimaryButtonClass} disabled={busy} type="submit">
          {busy ? "Saving..." : submitLabel}
        </button>
        <button className={secretButtonClass} disabled={busy} onClick={onCancel} type="button">
          Cancel
        </button>
      </div>
    </form>
  );
}
