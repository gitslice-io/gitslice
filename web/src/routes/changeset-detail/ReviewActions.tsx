import { type FormEvent } from "react";
import { cn } from "../../lib/cn";
import { isMergeableStatus } from "./status";
import { dangerButtonClass } from "./status";
import { MoreActionsMenu } from "./MoreActionsMenu";

export function ReviewActions({
  abandonPending,
  abandonReason,
  actionBusy,
  canMerge,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onMerge,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  canMerge: boolean;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onMerge(): void;
  terminal: boolean;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 lg:justify-end">
      <button
        className="h-8 rounded-md bg-zinc-950 dark:bg-zinc-100 px-3 py-2 text-xs font-medium text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
        disabled={actionBusy || terminal || !canMerge}
        onClick={onMerge}
        type="button"
      >
        {mergePending ? "Merging..." : "Merge"}
      </button>

      {!terminal ? (
        <MoreActionsMenu disabled={actionBusy} label="More changeset actions">
          <form className="space-y-2" onSubmit={onAbandon}>
            <label className="block text-xs font-medium text-slate-600 dark:text-zinc-400">
              Abandon changeset
              <input
                className="mt-1 h-9 w-full rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 text-sm text-zinc-950 dark:text-zinc-50 outline-none transition placeholder:text-slate-400 dark:placeholder:text-zinc-500 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700 disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800"
                disabled={actionBusy}
                onChange={(event) => onAbandonReasonChange(event.target.value)}
                placeholder="Optional reason"
                value={abandonReason}
              />
            </label>
            <button
              className={cn(dangerButtonClass, "w-full justify-center")}
              disabled={actionBusy}
              type="submit"
            >
              {abandonPending ? "Abandoning..." : "Abandon"}
            </button>
          </form>
        </MoreActionsMenu>
      ) : null}
    </div>
  );
}