import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";

import type { TreeEntry } from "../../api/types";
import {
  completionTarget,
  matchingFolders,
  type CopyMoveSource,
} from "../../components/source/slice-editing/copyMove";
import { normalizeRepositoryPath } from "../../components/source/sourceUtils";
import { cn } from "../../lib/cn";

const SUGGESTION_DELAY_MS = 120;
const MAX_SUGGESTIONS = 50;

interface PathComboboxProps {
  label: string;
  // listFolder lists a folder for suggestions; it may reject when the folder
  // is missing or unreadable, which shows none.
  listFolder(directory: string): Promise<TreeEntry[]>;
  onChange(value: string): void;
  source: CopyMoveSource;
  value: string;
}

// PathCombobox is a path input that suggests the folders at the level being
// typed. Choosing one completes the name with a trailing slash and moves on to
// that folder's folders, so a path can be built a level at a time.
export function PathCombobox({
  label,
  listFolder,
  onChange,
  source,
  value,
}: PathComboboxProps) {
  const listId = useId();
  const [touched, setTouched] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  const [focused, setFocused] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const [names, setNames] = useState<string[]>([]);
  // active is the highlighted suggestion; -1 until an arrow key picks one, so
  // Enter submits the dialog unless a folder was chosen.
  const [active, setActive] = useState(-1);
  const cache = useRef(new Map<string, Promise<TreeEntry[]>>());
  const target = completionTarget(value, source);
  const resolvable = target !== null;
  const directory = target?.directory ?? "";
  const prefix = target?.prefix ?? "";
  const sourcePath = normalizeRepositoryPath(source.path);

  // The dialog focuses its close button when it opens; take the focus after
  // that, with the name selected so typing replaces it and keeps the folder.
  useEffect(() => {
    const timer = window.setTimeout(() => {
      const input = inputRef.current;
      if (!input) {
        return;
      }
      input.focus();
      input.setSelectionRange(
        input.value.lastIndexOf("/") + 1,
        input.value.length,
      );
    }, 0);
    return () => window.clearTimeout(timer);
  }, []);

  useEffect(() => {
    if (!touched || !resolvable) {
      setNames([]);
      return;
    }
    let cancelled = false;
    const timer = window.setTimeout(() => {
      let listing = cache.current.get(directory);
      if (!listing) {
        listing = listFolder(directory).catch(() => []);
        cache.current.set(directory, listing);
      }
      void listing.then((entries) => {
        if (cancelled) {
          return;
        }
        const base = directory === "/" ? "" : directory;
        setNames(
          matchingFolders(entries, prefix)
            // The source itself is never a place to put it.
            .filter((name) => `${base}/${name}` !== sourcePath)
            .slice(0, MAX_SUGGESTIONS),
        );
        setActive(-1);
      });
    }, SUGGESTION_DELAY_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [directory, prefix, resolvable, touched, listFolder, sourcePath]);

  const open = focused && !dismissed && names.length > 0;

  function choose(name: string) {
    if (!target) {
      return;
    }
    onChange(`${target.head}${name}/`);
    setDismissed(false);
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (!open) {
      return;
    }
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        setActive((index) => (index + 1) % names.length);
        break;
      case "ArrowUp":
        event.preventDefault();
        setActive((index) => (index <= 0 ? names.length - 1 : index - 1));
        break;
      case "Enter":
        if (active >= 0) {
          event.preventDefault();
          choose(names[active]);
        }
        break;
      case "Tab":
        event.preventDefault();
        choose(names[Math.max(active, 0)]);
        break;
      case "Escape":
        // Close the list, not the dialog.
        event.preventDefault();
        event.stopPropagation();
        setDismissed(true);
        break;
    }
  }

  return (
    <div className="grid gap-1">
      <label
        className="text-sm font-medium text-slate-700 dark:text-zinc-300"
        htmlFor={`${listId}-input`}
      >
        {label}
      </label>
      <input
        aria-activedescendant={
          open && active >= 0 ? `${listId}-${active}` : undefined
        }
        aria-autocomplete="list"
        aria-controls={listId}
        aria-expanded={open}
        autoComplete="off"
        className="h-10 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 font-mono text-sm text-zinc-950 dark:text-zinc-50 outline-none transition placeholder:text-slate-400 dark:placeholder:text-zinc-500 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700"
        id={`${listId}-input`}
        onBlur={() => setFocused(false)}
        onChange={(event) => {
          setTouched(true);
          setDismissed(false);
          onChange(event.target.value);
        }}
        onFocus={() => setFocused(true)}
        onKeyDown={onKeyDown}
        ref={inputRef}
        role="combobox"
        spellCheck={false}
        value={value}
      />
      {open ? (
        <ul
          aria-label="Folders"
          className="max-h-48 overflow-y-auto rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 py-1 text-sm shadow-sm"
          id={listId}
          role="listbox"
        >
          {names.map((name, index) => (
            <li
              aria-selected={index === active}
              className={cn(
                "cursor-pointer truncate px-3 py-1.5 font-mono text-slate-700 dark:text-zinc-300",
                index === active &&
                  "bg-slate-100 dark:bg-zinc-800 text-zinc-950 dark:text-zinc-50",
              )}
              id={`${listId}-${index}`}
              key={name}
              // mousedown, so the input keeps focus and the list stays open.
              onMouseDown={(event) => {
                event.preventDefault();
                choose(name);
              }}
              onMouseEnter={() => setActive(index)}
              role="option"
            >
              {name}/
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
