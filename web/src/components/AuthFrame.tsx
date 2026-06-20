import type { ReactNode } from "react";

import { Badge, Card } from "./ui";

interface AuthFrameProps {
  eyebrow?: string;
  title: string;
  children: ReactNode;
}

export function AuthFrame({
  eyebrow = "Gitslice",
  title,
  children
}: AuthFrameProps) {
  return (
    <main className="grid min-h-[100dvh] place-items-center bg-surface px-4 py-8 text-on-surface sm:px-6">
      <Card
        as="section"
        className="w-full max-w-md overflow-visible"
        level="low"
        padding="lg"
      >
        <Badge variant="tertiary">{eyebrow}</Badge>
        <h1 className="mt-4 text-2xl font-semibold leading-tight sm:text-3xl">
          {title}
        </h1>
        <div className="mt-6 text-sm leading-6 text-on-surface-variant">
          {children}
        </div>
      </Card>
    </main>
  );
}
