import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import type { Secret, SecretScope } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { getErrorMessage } from "../slices/SlicePageParts";

// Secrets (design/33_secrets.md): values passed to the CI checks of the slices
// each one is scoped to, as environment variables.

export const SECRET_NAME_PATTERN = /^[A-Z_][A-Z0-9_]*$/;
export const MAX_SECRET_VALUE_BYTES = 48 * 1024;

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
  allSlices: boolean;
  // Slice names in the secret's account.
  slices: string[];
}

export function draftFromSecret(secret: Secret): SecretDraft {
  return {
    allSlices: Boolean(secret.scope?.allSlices),
    name: secret.name ?? "",
    slices: (secret.scope?.slices ?? []).map((ref) => ref.slice ?? "").filter(Boolean),
    value: ""
  };
}

export function scopeFromDraft(account: string, draft: SecretDraft): SecretScope {
  return draft.allSlices
    ? { allSlices: true }
    : { slices: draft.slices.map((slice) => ({ account, slice })) };
}

// scopeLabel says in words which slices a secret reaches.
export function scopeLabel(scope: SecretScope | undefined) {
  if (scope?.allSlices) return "All slices";
  const slices = (scope?.slices ?? []).map((ref) => ref.slice ?? "").filter(Boolean);
  return slices.length === 0 ? "Not used by any slice" : slices.join(", ");
}

export const SECRET_EXPOSURE_NOTE =
  "Anyone who can write to these slices can read the secret from a check, and so can the slices' CI daemons.";

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
  const [filter, setFilter] = useState("");

  const slicesQuery = useQuery({
    queryKey: ["secretScopeSlices", account],
    queryFn: async () =>
      ((await api.listSlices({ account, pageSize: 200 })).slices ?? [])
        .map((slice) => slice.ref?.slice ?? "")
        .filter(Boolean)
  });
  // Slices the secret lists stay choosable even if the listing missed them.
  const choices = Array.from(new Set([...(slicesQuery.data ?? []), ...initial.slices])).sort();
  const shown = filter ? choices.filter((slice) => slice.includes(filter.trim().toLowerCase())) : choices;

  function toggle(slice: string) {
    setDraft((current) => ({
      ...current,
      slices: current.slices.includes(slice)
        ? current.slices.filter((name) => name !== slice)
        : [...current.slices, slice].sort()
    }));
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
    if (!draft.allSlices && draft.slices.length === 0) {
      setProblem("Choose at least one slice, or all slices.");
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
        <legend className="mb-1 text-xs font-medium text-slate-600 dark:text-zinc-400">Slices</legend>
        <label className="flex items-start gap-2 text-sm text-zinc-900 dark:text-zinc-100">
          <input
            checked={!draft.allSlices}
            className="mt-1"
            disabled={busy}
            name="secret-scope"
            onChange={() => setDraft({ ...draft, allSlices: false })}
            type="radio"
          />
          <span>Selected slices of {account}</span>
        </label>
        {!draft.allSlices ? (
          <div className="ml-6 grid gap-2">
            {choices.length > 8 ? (
              <input
                aria-label="Filter slices"
                className={cn(inputClass, "h-8 text-xs")}
                onChange={(event) => setFilter(event.target.value)}
                placeholder="Filter slices"
                value={filter}
              />
            ) : null}
            {slicesQuery.isPending ? (
              <p className="text-xs text-slate-500 dark:text-zinc-400">Loading slices...</p>
            ) : slicesQuery.isError ? (
              <p className="text-xs text-rose-700 dark:text-rose-300">{getErrorMessage(slicesQuery.error)}</p>
            ) : choices.length === 0 ? (
              <p className="text-xs text-slate-500 dark:text-zinc-400">{account} has no slices yet.</p>
            ) : (
              <div className="grid max-h-56 gap-1 overflow-y-auto sm:grid-cols-2 lg:grid-cols-3">
                {shown.map((slice) => (
                  <label className="flex min-w-0 items-center gap-2 text-sm text-zinc-900 dark:text-zinc-100" key={slice}>
                    <input
                      checked={draft.slices.includes(slice)}
                      disabled={busy}
                      onChange={() => toggle(slice)}
                      type="checkbox"
                    />
                    <span className="truncate font-mono text-xs">{slice}</span>
                  </label>
                ))}
              </div>
            )}
          </div>
        ) : null}
        <label className="flex items-start gap-2 text-sm text-zinc-900 dark:text-zinc-100">
          <input
            checked={draft.allSlices}
            className="mt-1"
            disabled={busy}
            name="secret-scope"
            onChange={() => setDraft({ ...draft, allSlices: true })}
            type="radio"
          />
          <span>
            All slices of {account}
            <span className="block text-xs text-slate-500 dark:text-zinc-400">
              Including slices created later. A secret that lists a slice overrides this one there.
            </span>
          </span>
        </label>
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
