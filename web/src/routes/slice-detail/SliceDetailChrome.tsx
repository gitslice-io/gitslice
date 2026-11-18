import { Link } from "@tanstack/react-router";

import { MobileDetailsToggle } from "../../components/MobileDetails";
import { conversationsEnabled } from "../../lib/features";
import { CheckoutMenu } from "./CheckoutMenu";

export function SliceHeaderActions({
  sliceLabel,
  sliceRouteParams,
  isSignedIn,
  canConfigure,
  commitId,
  gitUrl,
  onOpenTriggers,
  onOpenFinder
}: {
  sliceLabel: string;
  sliceRouteParams: { account: string; slice: string } | null | undefined;
  isSignedIn: boolean | undefined;
  canConfigure: boolean;
  commitId: string;
  gitUrl: string;
  onOpenTriggers: () => void;
  onOpenFinder: () => void;
}) {
  return (
    <>
      <Link
        className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
        search={{ slice: sliceLabel }}
        to="/changesets"
      >
        Changesets
      </Link>
      {conversationsEnabled && isSignedIn && sliceRouteParams ? (
        <Link
          className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
          params={sliceRouteParams}
          to="/slices/$account/$slice/agents"
        >
          Conversations
        </Link>
      ) : null}
      {canConfigure && sliceRouteParams ? (
        <Link
          className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
          params={sliceRouteParams}
          to="/slices/$account/$slice/settings"
        >
          Settings
        </Link>
      ) : null}
      {isSignedIn && sliceRouteParams ? (
        <button
          className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
          onClick={onOpenTriggers}
          title="What a change in this folder sets off"
          type="button"
        >
          Triggers
        </button>
      ) : null}
      {commitId ? (
        <button
          className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
          onClick={onOpenFinder}
          title="Go to a file by name (t)"
          type="button"
        >
          Find file
        </button>
      ) : null}
      <CheckoutMenu gitUrl={gitUrl} sliceRef={sliceLabel} />
    </>
  );
}

export function MobileFileBar({ name, onBack }: { name: string; onBack(): void }) {
  return (
    <div className="sticky top-0 z-30 mb-3 flex items-center gap-2 border-b border-slate-200 dark:border-zinc-800 bg-slate-50/95 dark:bg-zinc-950/95 py-2 backdrop-blur lg:hidden">
      <button
        aria-label="Back to files"
        className="shrink-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
        onClick={onBack}
        type="button"
      >
        ← Files
      </button>
      <span
        className="min-w-0 flex-1 truncate font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50"
        title={name}
      >
        {name}
      </span>
      <MobileDetailsToggle />
    </div>
  );
}

export function CreatingChangesetOverlay() {
  return (
    <div
      aria-busy="true"
      aria-live="polite"
      className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/30 dark:bg-black/50 backdrop-blur-sm"
      role="status"
    >
      <div className="flex items-center gap-3 rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-5 py-4 shadow-xl">
        <span
          aria-hidden="true"
          className="h-5 w-5 animate-spin rounded-full border-2 border-slate-300 dark:border-zinc-700 border-t-zinc-900 dark:border-t-zinc-100"
        />
        <span className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          Creating changeset…
        </span>
      </div>
    </div>
  );
}

export function ChangesetErrorToast({
  message,
  onDismiss
}: {
  message: string;
  onDismiss: () => void;
}) {
  return (
    <div
      className="fixed inset-x-0 bottom-4 z-50 mx-auto w-[min(28rem,calc(100%-2rem))] rounded-md border border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/40 px-4 py-3 text-sm text-rose-900 dark:text-rose-200 shadow-lg"
      role="alert"
    >
      <div className="flex items-start justify-between gap-3">
        <span className="min-w-0 break-words">{message}</span>
        <button
          className="shrink-0 font-semibold underline decoration-rose-300 underline-offset-4 hover:decoration-rose-700"
          onClick={onDismiss}
          type="button"
        >
          Dismiss
        </button>
      </div>
    </div>
  );
}
