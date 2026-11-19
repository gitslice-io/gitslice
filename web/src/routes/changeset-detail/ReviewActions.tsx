import { type FormEvent } from "react";
import { cn } from "../../lib/cn";
import { dangerButtonClass } from "./status";
import { MoreActionsMenu } from "./MoreActionsMenu";

export function ReviewActions({
  abandonPending,
  abandonReason,
  actionBusy,
  approvePending = false,
  canAbandon = true,
  canApprove = false,
  canMerge,
  canSubmit = true,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onApprove,
  onMerge,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  approvePending?: boolean;
  canAbandon?: boolean;
  canApprove?: boolean;
  canMerge: boolean;
  canSubmit?: boolean;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onApprove?(): void;
  onMerge(): void;
  terminal: boolean;
}) {
  const showApprove = Boolean(!terminal && canApprove && onApprove);
  const showAbandon = !terminal && canAbandon;
  if (!canSubmit && !showApprove && !showAbandon) {
    return null;
  }

  return (
    <div className="flex flex-wrap items-center gap-2 lg:justify-end">
      {showApprove ? (
        <button
          className="h-8 rounded-md border border-emerald-300 dark:border-emerald-800 bg-emerald-50 dark:bg-emerald-950/40 px-3 py-1.5 text-xs font-semibold text-emerald-900 dark:text-emerald-200 transition hover:bg-emerald-100 dark:hover:bg-emerald-950/70 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
          disabled={actionBusy}
          onClick={onApprove}
          type="button"
        >
          {approvePending ? "Approving..." : "Approve"}
        </button>
      ) : null}

      {canSubmit ? (
        <button
          className="h-8 rounded-md bg-zinc-950 dark:bg-zinc-100 px-3 py-2 text-xs font-medium text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
          disabled={actionBusy || terminal || !canMerge}
          onClick={onMerge}
          type="button"
        >
          {mergePending ? "Merging..." : "Merge"}
        </button>
      ) : null}

      {showAbandon ? (
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
