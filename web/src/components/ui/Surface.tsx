import { createElement, type HTMLAttributes } from "react";

import { cn } from "../../lib/cn";

export type SurfaceLevel = "lowest" | "low" | "base" | "high" | "highest" | "dim";
export type SurfaceElement = "div" | "section" | "article" | "aside" | "header" | "nav";

export interface SurfaceStyleOptions {
  level?: SurfaceLevel;
  className?: string;
}

export interface SurfaceProps extends HTMLAttributes<HTMLElement>, SurfaceStyleOptions {
  as?: SurfaceElement;
}

const levelClassNames: Record<SurfaceLevel, string> = {
  lowest: "bg-surface-container-lowest",
  low: "bg-surface-container-low",
  base: "bg-surface-container",
  high: "bg-surface-container-high",
  highest: "bg-surface-container-highest",
  dim: "bg-surface-dim"
};

export function surfaceClassName({
  level = "base",
  className
}: SurfaceStyleOptions = {}) {
  return cn("rounded-sm text-on-surface transition-colors", levelClassNames[level], className);
}

export function Surface({
  as: Component = "div",
  className,
  level = "base",
  ...props
}: SurfaceProps) {
  return createElement(Component, {
    className: surfaceClassName({ className, level }),
    ...props
  });
}
