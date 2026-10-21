import { useState, type FormEvent } from "react";

import { ErrorBox } from "./ErrorBox";

const MAX_TITLE = 200;

// InfoEditor edits a changeset's title (one line, like a commit subject) and
// its description (Markdown). It is shown in place of both while editing.
export function InfoEditor({
  description,
  onCancel,
  onSave,
  title
}: {
  description: string;
  onCancel(): void;
  onSave(title: string, description: string): Promise<void>;
  title: string;
}) {
  const [draftTitle, setDraftTitle] = useState(title);
  const [draftDescription, setDraftDescription] = useState(description);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const trimmed = draftTitle.trim();
  const unchanged = trimmed === title && draftDescription.trim() === description.trim();

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!trimmed || saving) return;
    setSaving(true);
    setError("");
    try {
      await onSave(trimmed, draftDescription.trim());
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save the changes.");
      setSaving(false);
    }
  }

  const field =
    "w-full rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-950 px-3 py-2 text-sm text-zinc-950 dark:text-zinc-50 outline-none focus:border-slate-500 dark:focus:border-zinc-500";

  return (
    <form className="grid gap-3" onSubmit={(event) => void submit(event)}>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Title
        <input
          autoFocus
          className={field}
          maxLength={MAX_TITLE}
          onChange={(event) => setDraftTitle(event.target.value)}
          value={draftTitle}
        />
        <span className="font-normal text-slate-500 dark:text-zinc-500">
          One line, like a commit subject ({draftTitle.length}/{MAX_TITLE}).
        </span>
      </label>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Description
        <textarea
          className={`${field} min-h-32 font-mono`}
          onChange={(event) => setDraftDescription(event.target.value)}
          placeholder="What changed and why. Markdown is supported."
          value={draftDescription}
        />
      </label>
      {error ? <ErrorBox message={error} /> : null}
      <div className="flex gap-2">
        <button
          className="rounded-md bg-zinc-950 dark:bg-zinc-50 px-3 py-1.5 text-sm font-medium text-white dark:text-zinc-950 disabled:opacity-50"
          disabled={!trimmed || unchanged || saving}
          type="submit"
        >
          {saving ? "Saving…" : "Save"}
        </button>
        <button
          className="rounded-md border border-slate-200 dark:border-zinc-800 px-3 py-1.5 text-sm font-medium text-slate-700 dark:text-zinc-300"
          disabled={saving}
          onClick={onCancel}
          type="button"
        >
          Cancel
        </button>
      </div>
    </form>
  );
}
