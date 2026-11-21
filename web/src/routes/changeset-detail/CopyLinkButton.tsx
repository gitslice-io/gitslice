import { useState } from "react";
import { shortChangesetId } from "../../lib/objectId";

export function CopyLinkButton({ changesetId }: { changesetId: string }) {
  const [copied, setCopied] = useState(false);
  const shareId = shortChangesetId(changesetId);

  if (!shareId) {
    return null;
  }

  const shareUrl =
    typeof window !== "undefined"
      ? `${window.location.origin}/cs/${shareId}`
      : `/cs/${shareId}`;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(shareUrl);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
    }
  };

  return (
    <button
      className="inline-flex items-center gap-1.5 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2 py-1 text-xs font-medium text-slate-600 dark:text-zinc-400 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:text-zinc-950 dark:hover:text-zinc-50"
      onClick={copy}
      title={shareUrl}
      type="button"
    >
      {copied ? (
        <svg
          aria-hidden="true"
          className="h-3.5 w-3.5 text-emerald-600 dark:text-emerald-400"
          fill="none"
          stroke="currentColor"
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth="2"
          viewBox="0 0 24 24"
        >
          <polyline points="20 6 9 17 4 12" />
        </svg>
      ) : (
        <svg
          aria-hidden="true"
          className="h-3.5 w-3.5"
          fill="none"
          stroke="currentColor"
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth="1.75"
          viewBox="0 0 24 24"
        >
          <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
          <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />
        </svg>
      )}
      {copied ? "Link copied" : "Copy link"}
    </button>
  );
}