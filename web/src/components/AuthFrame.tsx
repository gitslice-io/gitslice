import type { ReactNode } from "react";

interface AuthFrameProps {
  eyebrow?: string;
  title: string;
  children: ReactNode;
}

export function AuthFrame({ eyebrow = "Gitslice", title, children }: AuthFrameProps) {
  return (
    <main className="grid min-h-[100dvh] place-items-center bg-slate-50 p-6 text-zinc-900">
      <section className="w-full max-w-md rounded-lg border border-slate-200 bg-white p-6 shadow-sm">
        <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
          {eyebrow}
        </p>
        <h1 className="mt-2 text-2xl font-semibold tracking-normal">{title}</h1>
        <div className="mt-6">{children}</div>
      </section>
    </main>
  );
}
