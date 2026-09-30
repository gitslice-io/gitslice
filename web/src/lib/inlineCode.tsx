import type { ReactNode } from "react";

import { cn } from "./cn";

const inlineCodeClassName =
  "rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300";

export function renderInlineCode(text: string, className?: string): ReactNode {
  const parts: ReactNode[] = [];
  const pattern = /`([^`]+)`/g;
  let lastIndex = 0;
  let match = pattern.exec(text);

  while (match) {
    if (match.index > lastIndex) {
      parts.push(text.slice(lastIndex, match.index));
    }

    parts.push(
      <code className={cn(inlineCodeClassName, className)} key={match.index}>
        {match[1]}
      </code>
    );
    lastIndex = pattern.lastIndex;
    match = pattern.exec(text);
  }

  if (lastIndex < text.length) {
    parts.push(text.slice(lastIndex));
  }

  return parts.length > 0 ? parts : text;
}
