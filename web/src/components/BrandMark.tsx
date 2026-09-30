import { cn } from "../lib/cn";

interface BrandMarkProps {
  className?: string;
}

export function BrandMark({ className }: BrandMarkProps) {
  return (
    // The mark is black on transparent: invert it on dark backgrounds. The
    // 96px copy covers every size it is shown at (up to 28px at 3x) for a
    // fraction of the 512px original's bytes.
    <img
      alt=""
      aria-hidden="true"
      className={cn("shrink-0 dark:invert", className)}
      decoding="async"
      height={96}
      src="/brand/gitslice-mark-96.png"
      width={96}
    />
  );
}
