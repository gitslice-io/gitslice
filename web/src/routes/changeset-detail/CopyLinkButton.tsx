import { CopyButton } from "../../components/ui/CopyButton";
import { shortChangesetId } from "../../lib/objectId";

export function CopyLinkButton({ changesetId }: { changesetId: string }) {
  const shareId = shortChangesetId(changesetId);

  if (!shareId) {
    return null;
  }

  const shareUrl =
    typeof window !== "undefined"
      ? `${window.location.origin}/cs/${shareId}`
      : `/cs/${shareId}`;

  return (
    <CopyButton
      className="gap-1.5 border-slate-200 dark:border-zinc-800 px-2 hover:border-slate-300 dark:hover:border-zinc-700"
      copiedAnnouncement="Link copied"
      copiedLabel="Link copied"
      resetMs={1500}
      size="sm"
      title={shareUrl}
      value={shareUrl}
      variant="secondary"
    >
      {(status) => (
        <>
          {status === "copied" ? (
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
          {status === "copied"
            ? "Link copied"
            : status === "failed"
              ? "Copy failed"
              : "Copy link"}
        </>
      )}
    </CopyButton>
  );
}
