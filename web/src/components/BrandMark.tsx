import { cn } from "../lib/cn";

interface BrandMarkProps {
  className?: string;
}

// The mark is black on a transparent background, so it is inverted in dark
// mode; a caller on an always-dark surface passes "invert". It is drawn at
// 20 to 28px, so the 96px file covers high-density screens.
export function BrandMark({ className }: BrandMarkProps) {
  return (
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
