import { useMemo } from "react";

import { cn } from "../lib/cn";
import { useInternalLinkClickHandler } from "../lib/internalLinkNavigation";
import { renderMarkdownToHtml } from "../lib/markdown";

// Renders an agent message as sanitized markdown. Parsing is synchronous so
// streaming messages re-render cleanly on every token, and internal links
// navigate via the app router.
export function MessageMarkdown({
  className,
  source
}: {
  className?: string;
  source: string;
}) {
  const html = useMemo(() => renderMarkdownToHtml(source), [source]);
  const onLinkClick = useInternalLinkClickHandler();
  return (
    <div
      className={cn(
        "prose prose-sm prose-slate dark:prose-invert max-w-none break-words prose-p:my-2 prose-pre:my-2 prose-pre:border prose-pre:border-slate-200 dark:prose-pre:border-zinc-800 prose-pre:bg-slate-50 dark:prose-pre:bg-zinc-950 prose-pre:text-zinc-900 dark:prose-pre:text-zinc-100 prose-code:before:content-none prose-code:after:content-none prose-headings:mb-2 prose-headings:mt-3 first:prose-headings:mt-0 prose-ul:my-2 prose-ol:my-2 prose-li:my-0.5 prose-a:text-sky-700 dark:prose-a:text-sky-300 prose-a:underline [&>:first-child]:mt-0 [&>:last-child]:mb-0",
        className
      )}
      dangerouslySetInnerHTML={{ __html: html }}
      onClick={onLinkClick}
    />
  );
}
