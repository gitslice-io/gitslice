import { cn } from "../lib/cn";

interface BrandMarkProps {
  className?: string;
}

export function BrandMark({ className }: BrandMarkProps) {
  return (
    <img
      alt=""
      aria-hidden="true"
      className={cn("shrink-0", className)}
      decoding="async"
      height={512}
      src="/brand/gitslice-mark.png"
      width={512}
    />
  );
}
