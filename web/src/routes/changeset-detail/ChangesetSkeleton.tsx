export function ChangesetSkeleton() {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5 shadow-sm shadow-slate-200/50 dark:shadow-black/50">
        <div className="h-4 w-48 animate-pulse rounded bg-slate-200 dark:bg-zinc-700" />
        <div className="mt-5 h-8 w-2/3 animate-pulse rounded bg-slate-200 dark:bg-zinc-700" />
        <div className="mt-4 flex flex-wrap gap-2">
          <div className="h-7 w-24 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
          <div className="h-7 w-32 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
          <div className="h-7 w-20 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
        </div>
      </div>
    </section>
  );
}