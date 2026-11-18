import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { shortChangesetId } from "../../../lib/objectId";
import { Button } from "../../ui/Button";
import {
  draftStatusLabel,
  errorMessageFromUnknown,
  pendingEditLabel
} from "./draftChangesetHelpers";
import { pendingEditKey } from "./pendingEdits";
import type { DraftChangesetController } from "./useDraftChangesetController";

export function DraftChangesetPanel({
  actionStatus,
  changesetId,
  changesetLabel,
  currentPatchsetId,
  edits,
  errorMessage,
  removeEdit,
  retrySave,
  saveStatus,
  discardDraft,
  submitDraft
}: DraftChangesetController) {
  const navigate = useNavigate();
  const [actionError, setActionError] = useState("");
  const shareId = shortChangesetId(changesetId);
  const detailId = shareId || changesetId;
  const visibleLabel = changesetLabel || detailId;
  const isAdopting = saveStatus === "adopting";
  const isSaving = saveStatus === "saving";
  const isActionPending = actionStatus !== "idle";
  const hasDraft = Boolean(changesetId || edits.length);

  if (!hasDraft && !isAdopting && !errorMessage) {
    return null;
  }

  async function submit() {
    setActionError("");
    try {
      const id = await submitDraft();
      void navigate({
        to: "/cs/$id",
        params: { id: shortChangesetId(id) }
      });
    } catch (error) {
      setActionError(errorMessageFromUnknown(error));
    }
  }

  async function discard() {
    setActionError("");
    try {
      await discardDraft();
    } catch (error) {
      setActionError(errorMessageFromUnknown(error));
    }
  }

  return (
    <section className="rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5 shadow-sm shadow-slate-200/50 dark:shadow-black/50">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
            Draft changeset
          </h2>
          <div className="mt-1 flex min-w-0 flex-wrap items-center gap-2 text-sm text-slate-600 dark:text-zinc-400">
            {detailId ? (
              <Link
                className="break-all font-mono text-zinc-900 dark:text-zinc-100 underline decoration-slate-300 underline-offset-4 hover:decoration-slate-700"
                params={{ id: detailId }}
                to="/cs/$id"
              >
                {visibleLabel}
              </Link>
            ) : (
              <span>Draft will be created with the first saved edit.</span>
            )}
            <span className="inline-flex rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-2 py-1 text-xs font-semibold text-slate-700 dark:text-zinc-300">
              {draftStatusLabel(saveStatus)}
            </span>
          </div>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          <Button
            disabled={isActionPending || isAdopting || isSaving || !hasDraft}
            onClick={() => void discard()}
            size="md"
            variant="secondary"
          >
            {actionStatus === "discarding" ? "Discarding..." : "Discard"}
          </Button>
          <Button
            disabled={
              isActionPending ||
              isAdopting ||
              isSaving ||
              !changesetId ||
              !currentPatchsetId ||
              !edits.length
            }
            onClick={() => void submit()}
            size="md"
            variant="primary"
          >
            {actionStatus === "submitting" ? "Submitting..." : "Submit"}
          </Button>
        </div>
      </div>

      {edits.length ? (
        <ul className="mt-4 divide-y divide-slate-100 dark:divide-zinc-800 rounded-lg border border-slate-200 dark:border-zinc-800">
          {edits.map((edit) => {
            const key = pendingEditKey(edit);
            return (
              <li
                className="flex min-w-0 flex-col gap-3 p-3 sm:flex-row sm:items-center sm:justify-between"
                key={key}
              >
                <div className="min-w-0">
                  <span className="inline-flex rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-2 py-1 text-xs font-semibold text-slate-700 dark:text-zinc-300">
                    {pendingEditLabel(edit)}
                  </span>
                  <div className="mt-2 break-all font-mono text-sm text-zinc-950 dark:text-zinc-50">
                    {edit.kind === "rename" ? (
                      <>
                        {edit.oldPath} <span className="text-slate-400 dark:text-zinc-500">to</span>{" "}
                        {edit.path}
                      </>
                    ) : (
                      edit.path
                    )}
                  </div>
                </div>
                <Button
                  className="w-fit"
                  disabled={isActionPending || isAdopting}
                  onClick={() => removeEdit(key)}
                  size="sm"
                  variant="secondary"
                >
                  Remove
                </Button>
              </li>
            );
          })}
        </ul>
      ) : (
        <p className="mt-4 rounded-lg border border-dashed border-slate-300 dark:border-zinc-700 p-3 text-sm text-slate-600 dark:text-zinc-400">
          No file edits in this draft.
        </p>
      )}

      {errorMessage ? (
        <p className="mt-3 rounded-md border border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 px-3 py-2 text-sm text-rose-900 dark:text-rose-200">
          {errorMessage}
          <button
            className="ml-3 font-semibold underline decoration-rose-300 underline-offset-4 hover:decoration-rose-800 disabled:cursor-not-allowed disabled:opacity-60"
            disabled={isActionPending || isAdopting || isSaving}
            onClick={retrySave}
            type="button"
          >
            Retry
          </button>
        </p>
      ) : null}
      {actionError ? (
        <p className="mt-3 rounded-md border border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 px-3 py-2 text-sm text-rose-900 dark:text-rose-200">
          {actionError}
        </p>
      ) : null}
    </section>
  );
}
