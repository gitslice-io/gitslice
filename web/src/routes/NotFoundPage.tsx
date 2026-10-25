import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

// What a page says when the thing it was opened for does not exist. The server
// sends it with a 404 for a visitor with no session (crawlers, curl, agents),
// so a missing slice is not mistaken for a page.
export function NotFoundPage({
  children,
  title = "Page not found"
}: {
  children?: ReactNode;
  title?: string;
}) {
  return (
    <section className="mx-auto w-full max-w-xl px-1 py-12 text-center sm:py-16">
      <p className="text-sm font-semibold text-slate-500 dark:text-zinc-400">404</p>
      <h1 className="mt-2 text-2xl font-semibold text-zinc-950 dark:text-zinc-50">{title}</h1>
      <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
        {children ?? "There is nothing at this address. It may have moved, or the link may be mistyped."}
      </p>
      <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
        <Link
          className="inline-flex min-h-9 items-center rounded-md bg-zinc-950 px-3 py-1.5 text-sm font-semibold text-white transition hover:bg-zinc-800 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
          to="/"
        >
          Go to Gitslice
        </Link>
        <Link
          className="inline-flex min-h-9 items-center rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
          to="/login"
        >
          Sign in
        </Link>
      </div>
    </section>
  );
}

// For an address that matches no page.
export function PageNotFound() {
  return <NotFoundPage />;
}

const privateHint = "It may not exist, or it may be private: if it is yours, sign in to see it.";

export function SliceNotFound() {
  return <NotFoundPage title="No such slice">{privateHint}</NotFoundPage>;
}

export function ChangesetNotFound() {
  return <NotFoundPage title="No such changeset">{privateHint}</NotFoundPage>;
}

export function AccountNotFound() {
  return <NotFoundPage title="No such account">There is no account at this address.</NotFoundPage>;
}
