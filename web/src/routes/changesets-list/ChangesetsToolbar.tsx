import { useEffect, useState, type FormEvent } from "react";

import { SliceNotice } from "../../components/slices/SlicePageParts";
import { Input } from "../../components/ui/Input";
import { cn } from "../../lib/cn";
import { statusTabs, type StatusTab } from "./filters";

// ChangesetsToolbar picks which changesets to list: a status group, and text
// to find in titles and ids or `author:name`. Typing updates the address after
// a short pause, so each keystroke is not a request.
export function ChangesetsToolbar({
  onSearch,
  onTab,
  tab,
  typed
}: {
  onSearch(q: string): void;
  onTab(tab: StatusTab): void;
  tab: StatusTab;
  typed: string;
}) {
  const [text, setText] = useState(typed);
  // The address changed by other means (back, a link): show it.
  useEffect(() => setText(typed), [typed]);
  useEffect(() => {
    if (text === typed) {
      return;
    }
    const timer = window.setTimeout(() => onSearch(text), 300);
    return () => window.clearTimeout(timer);
    // onSearch is a new function each render; only typing should restart this.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text]);

  return (
    <div className="mb-3 flex flex-wrap items-center gap-2">
      <div
        aria-label="Status"
        className="grid h-9 w-full grid-cols-4 overflow-hidden rounded-md border border-slate-200 bg-slate-50 p-0.5 dark:border-zinc-800 dark:bg-zinc-950 sm:inline-flex sm:w-fit"
        role="group"
      >
        {statusTabs.map((entry) => (
          <button
            aria-pressed={tab === entry.value}
            className={cn(
              "rounded px-2 text-center text-sm font-medium transition sm:px-3",
              tab === entry.value
                ? "bg-white text-zinc-950 shadow-sm dark:bg-zinc-900 dark:text-zinc-50"
                : "text-slate-600 hover:text-zinc-950 dark:text-zinc-400 dark:hover:text-zinc-50"
            )}
            key={entry.value}
            onClick={() => onTab(entry.value)}
            type="button"
          >
            {entry.label}
          </button>
        ))}
      </div>
      <Input
        aria-label="Search changesets"
        className="basis-full sm:max-w-sm sm:flex-1 sm:basis-0"
        onChange={(event) => setText(event.target.value)}
        placeholder="Search titles and ids, or author:name"
        type="search"
        value={text}
      />
    </div>
  );
}

export function MissingSliceState({
  navigateToChangeset
}: {
  navigateToChangeset(id: string): void;
}) {
  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <SliceNotice title="Open a slice first">
        Use a slice page&apos;s Changesets tab to open the slice-scoped list.
      </SliceNotice>
      <OpenChangesetForm onOpen={navigateToChangeset} />
    </div>
  );
}

function OpenChangesetForm({ onOpen }: { onOpen(id: string): void }) {
  const [changeset, setChangeset] = useState("");
  const [error, setError] = useState("");

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const id = changeset.trim();

    if (!id) {
      setError("Enter a changeset id.");
      return;
    }

    setError("");
    onOpen(id);
  };

  return (
    <form
      className="rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5 shadow-sm shadow-slate-200/50 dark:shadow-black/50"
      onSubmit={submit}
    >
      <label className="grid gap-2 text-sm font-medium text-zinc-800 dark:text-zinc-200">
        Open changeset
        <input
          className="h-11 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 text-sm text-zinc-950 dark:text-zinc-50 outline-none transition placeholder:text-slate-400 dark:placeholder:text-zinc-500 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700"
          onChange={(event) => setChangeset(event.target.value)}
          placeholder="3f9a2b1c4d"
          value={changeset}
        />
      </label>

      {error ? <p className="mt-2 text-sm text-red-700">{error}</p> : null}

      <button
        className="mt-5 rounded-md bg-zinc-950 dark:bg-zinc-100 px-4 py-2.5 text-sm font-medium text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:translate-y-px"
        type="submit"
      >
        Open
      </button>
    </form>
  );
}
