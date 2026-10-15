import { PageHeader } from "./DocParts";
import { conceptCards } from "./docData";

export function ConceptsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Concepts"
        description="Gitslice is native source storage first, Git compatibility second. These are the terms that explain how the system works."
      />

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        {conceptCards.map((concept) => (
          <article
            className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5"
            key={concept.title}
          >
            <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
              {concept.title}
            </h2>
            <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
              {concept.description}
            </p>
          </article>
        ))}
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          Submit lifecycle
        </h2>
        <div className="mt-4">
          <div className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
            {[
              ["Draft", "Edit and update patchsets."],
              ["Pending publish", "Submit accepted and queued."],
              ["Submitted", "Included in the accepted main tree."],
              ["Native commit", "Immutable snapshot behind refs/global/main."]
            ].map(([title, description]) => (
              <div
                className="rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 p-4"
                key={title}
              >
                <h3 className="font-semibold text-zinc-950 dark:text-zinc-50">{title}</h3>
                <p className="mt-2 leading-6 text-slate-600 dark:text-zinc-400">{description}</p>
              </div>
            ))}
          </div>
        </div>
      </section>
    </div>
  );
}
