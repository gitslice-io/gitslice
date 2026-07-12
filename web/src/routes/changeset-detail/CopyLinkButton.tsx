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
      <span aria-hidden="true">{copied ? "OK" : "URL"}</span>
      {copied ? "Link copied" : "Copy link"}
    </button>
  );
}