import type { ReactNode } from "react";

import { cn } from "../lib/cn";
import { BrandMark } from "./BrandMark";
import { ThemeToggle } from "./ThemeToggle";

interface AuthFrameProps {
  eyebrow?: string;
  title: string;
  // False where the page hosts a card with a heading of its own.
  titleIsHeading?: boolean;
  // The Clerk card this frame is about to hold. Its room is kept from the
  // first paint, so the frame does not grow (and, centered, jump) when the
  // card arrives a moment after the page.
  reserve?: keyof typeof reservedHeight;
  children: ReactNode;
}

// Clerk's cards as measured at phone and desktop widths.
const reservedHeight = {
  "sign-in": "min-h-[30rem] sm:min-h-[28.75rem]",
  "sign-up": "min-h-[35rem] sm:min-h-[34rem]"
} as const;

// The whole frame at those sizes (the card plus the frame's own heading and
// padding). The frame is centered on the page, and a browser can paint it
// while its HTML is still arriving: with only its first row parsed it was
// 84px tall, and jumped when the rest came. Its height is on the frame's own
// tag, so it is right from the first paint whatever has been parsed.
const reservedFrameHeight = {
  "sign-in": "min-h-[38.625rem] sm:min-h-[38.5rem]",
  "sign-up": "min-h-[43.75rem] sm:min-h-[43.625rem]"
} as const;

export function AuthFrame({
  eyebrow = "Gitslice",
  title,
  titleIsHeading = true,
  reserve,
  children
}: AuthFrameProps) {
  return (
    <main className="grid min-h-[100dvh] place-items-center bg-slate-50 p-4 text-zinc-900 transition-colors duration-200 dark:bg-zinc-950 dark:text-zinc-100 sm:p-6">
      <section
        className={cn(
          "w-full max-w-md overflow-hidden rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50 transition-colors duration-200 dark:border-zinc-800 dark:bg-zinc-900 dark:shadow-black/20 sm:p-6",
          reserve && reservedFrameHeight[reserve]
        )}
      >
        <div className="flex items-center justify-between gap-4">
          <p className="flex items-center gap-2 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
            <BrandMark className="size-5" />
            {eyebrow}
          </p>
          <ThemeToggle />
        </div>
        {/* The sign-in and sign-up cards carry their own h1 ("Sign in to
            Gitslice"), so theirs stays the page's only one. */}
        {titleIsHeading ? (
          <h1 className="mt-2 text-xl font-semibold tracking-normal sm:text-2xl">
            {title}
          </h1>
        ) : (
          <p className="mt-2 text-xl font-semibold tracking-normal sm:text-2xl">
            {title}
          </p>
        )}
        <div className={reserve ? `mt-6 ${reservedHeight[reserve]}` : "mt-6"}>{children}</div>
      </section>
    </main>
  );
}
