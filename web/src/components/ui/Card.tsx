import { type HTMLAttributes } from "react";

import { cn } from "../../lib/cn";
import { Surface, type SurfaceElement, type SurfaceLevel } from "./Surface";

export type CardPadding = "none" | "sm" | "md" | "lg";

export interface CardProps extends HTMLAttributes<HTMLElement> {
  as?: Extract<SurfaceElement, "div" | "section" | "article">;
  level?: SurfaceLevel;
  padding?: CardPadding;
}

const paddingClassNames: Record<CardPadding, string> = {
  none: "p-0",
  sm: "p-4",
  md: "p-5",
  lg: "p-6"
};

export function Card({
  as = "article",
  className,
  level = "low",
  padding = "md",
  ...props
}: CardProps) {
  return (
    <Surface
      as={as}
      className={cn("overflow-hidden", paddingClassNames[padding], className)}
      level={level}
      {...props}
    />
  );
}
