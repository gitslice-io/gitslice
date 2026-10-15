import { PageHeader, CommandBlock } from "./DocParts";
import { gitTranslations, gitQuestions } from "./docData";

export function GitUsersDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="For Git Users"
        description="Use this page if your mental model starts with repos, branches, commits, and pull requests. Gitslice keeps familiar boundaries but changes the native write path."
      />

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          Git to Gitslice
        </h2>
        <div className="mt-4 overflow-x-auto">
          <table className="min-w-full divide-y divide-slate-200 dark:divide-zinc-800 text-left text-sm">
            <thead className="bg-slate-50 dark:bg-zinc-950 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
              <tr>
                <th className="px-4 py-3">Git term</th>
                <th className="px-4 py-3">Gitslice term</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-zinc-800">
              {gitTranslations.map(([git, gitslice]) => (
                <tr key={git}>
                  <td className="px-4 py-3 font-medium text-zinc-950 dark:text-zinc-50">{git}</td>
                  <td className="px-4 py-3 text-slate-600 dark:text-zinc-400">{gitslice}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          Familiar workflow, native submit
        </h2>
        <CommandBlock>{`gs auth login
gs init <account>:<slice>
# edit files
gs status
gs create --message "change title"
gs submit`}</CommandBlock>
        <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          The important shift: you do not need to create a local commit before
          review. A changeset is the review unit, and submit creates the accepted
          native snapshot.
        </p>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        {gitQuestions.map((item) => (
          <article
            className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5"
            key={item.question}
          >
            <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
              {item.question}
            </h2>
            <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
              {item.answer}
            </p>
          </article>
        ))}
      </section>
    </div>
  );
}
