export function PageMessage({ message, title }: { message: string; title: string }) {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-6 shadow-sm shadow-slate-200/50 dark:shadow-black/50">
        <h1 className="text-xl font-semibold tracking-normal text-zinc-950 dark:text-zinc-50">
          {title}
        </h1>
        <p className="mt-2 text-sm text-slate-600 dark:text-zinc-400">{message}</p>
      </div>
    </section>
  );
}