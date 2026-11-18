import type { FormEvent } from "react";

import type { AgentDaemon } from "../../api/types";
import { daemonName } from "../../lib/daemons";
import { Popup } from "../Popup";

export function CreateConversationDialog({
  createError,
  isCreating,
  onClose,
  onSelectDaemon,
  onSubmit,
  onTitleChange,
  onlineDaemons,
  open,
  selectedDaemonId,
  sliceKey,
  title
}: {
  createError: string;
  isCreating: boolean;
  onClose: () => void;
  onSelectDaemon: (daemonId: string) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  onTitleChange: (title: string) => void;
  onlineDaemons: AgentDaemon[];
  open: boolean;
  selectedDaemonId: string;
  sliceKey: string;
  title: string;
}) {
  return (
    <Popup
      description={sliceKey}
      onClose={onClose}
      open={open}
      title="New conversation"
    >
      {onlineDaemons.length ? (
        <form className="grid gap-3" onSubmit={onSubmit}>
          <label className="grid gap-1.5 text-sm font-medium text-zinc-800 dark:text-zinc-200">
            Agent daemon
            <select
              className="min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm text-zinc-950 dark:text-zinc-50 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700"
              onChange={(event) => onSelectDaemon(event.target.value)}
              value={selectedDaemonId}
            >
              {onlineDaemons.map((daemon) => (
                <option
                  key={daemon.id ?? daemon.name}
                  value={daemon.id ?? ""}
                >
                  {daemonName(daemon)}
                </option>
              ))}
            </select>
          </label>
          <label className="grid gap-1.5 text-sm font-medium text-zinc-800 dark:text-zinc-200">
            Title
            <input
              className="min-w-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm text-zinc-950 dark:text-zinc-50 outline-none transition placeholder:text-slate-400 dark:placeholder:text-zinc-500 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700"
              onChange={(event) => onTitleChange(event.target.value)}
              placeholder="Optional"
              value={title}
            />
          </label>
          {createError ? (
            <p className="text-sm text-rose-700 dark:text-rose-300">{createError}</p>
          ) : null}
          <div className="mt-1 flex justify-end gap-2">
            <button
              className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
              onClick={onClose}
              type="button"
            >
              Cancel
            </button>
            <button
              className="rounded-md bg-zinc-950 dark:bg-zinc-100 px-3 py-2 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300 dark:disabled:bg-zinc-600"
              disabled={!selectedDaemonId || isCreating}
              type="submit"
            >
              {isCreating ? "Creating..." : "Create conversation"}
            </button>
          </div>
        </form>
      ) : (
        <p className="text-sm text-slate-600 dark:text-zinc-400">
          No online daemons are available right now.
        </p>
      )}
    </Popup>
  );
}
