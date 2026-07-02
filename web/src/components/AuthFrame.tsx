import type { ReactNode } from "react";

interface AuthFrameProps {
  eyebrow?: string;
  title: string;
  children: ReactNode;
}

export function AuthFrame({ eyebrow = "Gitslice", title, children }: AuthFrameProps) {
  return (
    <main className="grid min-h-[100dvh] place-items-center bg-slate-50 p-4 text-zinc-900 sm:p-6">
      <section className="w-full max-w-md overflow-hidden rounded-lg border border-slate-200 bg-white p-4 shadow-sm sm:p-6">
        <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
          {eyebrow}
        </p>
        <h1 className="mt-2 text-xl font-semibold tracking-normal sm:text-2xl">
          {title}
        </h1>
        <div className="mt-6">{children}</div>
      </section>
    </main>
  );
}
