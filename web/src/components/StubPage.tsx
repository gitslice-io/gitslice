interface StubPageProps {
  title: string;
}

export function StubPage({ title }: StubPageProps) {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="border-b border-slate-200 dark:border-zinc-800 pb-5">
        <p className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
          Gitslice Web
        </p>
        <h1 className="mt-2 break-words text-xl font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-2xl">
          {title}
        </h1>
      </div>
      <div className="mt-8 rounded-lg border border-dashed border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 p-6 text-sm text-slate-600 dark:text-zinc-400">
        TODO
      </div>
    </section>
  );
}
