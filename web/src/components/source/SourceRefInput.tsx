import { FormEvent, useEffect, useId, useState } from "react";

interface SourceRefInputProps {
  disabled?: boolean;
  isCommitMode: boolean;
  value: string;
  onSubmit(value: string): void;
}

export function SourceRefInput({
  disabled = false,
  isCommitMode,
  value,
  onSubmit
}: SourceRefInputProps) {
  const inputId = useId();
  const [draft, setDraft] = useState(value);

  useEffect(() => {
    setDraft(value);
  }, [value]);

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    onSubmit(draft);
  }

  return (
    <form className="grid gap-1 sm:min-w-80" onSubmit={handleSubmit}>
      <label
        className="text-xs font-semibold uppercase tracking-normal text-slate-500"
        htmlFor={inputId}
      >
        {isCommitMode ? "Commit" : "Ref or commit"}
      </label>
      <div className="flex min-w-0 gap-2">
        <input
          className="h-10 min-w-0 flex-1 rounded-md border border-slate-300 bg-white px-3 font-mono text-sm text-zinc-900 outline-none transition focus:border-slate-500 disabled:bg-slate-100 disabled:text-slate-500"
          disabled={disabled}
          id={inputId}
          onChange={(event) => setDraft(event.target.value)}
          placeholder="main or sha256:..."
          spellCheck={false}
          value={draft}
        />
        <button
          className="h-10 rounded-md border border-zinc-900 bg-zinc-950 px-4 text-sm font-semibold text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:border-slate-300 disabled:bg-slate-200 disabled:text-slate-500"
          disabled={disabled || draft.trim() === ""}
          type="submit"
        >
          Go
        </button>
      </div>
    </form>
  );
}

