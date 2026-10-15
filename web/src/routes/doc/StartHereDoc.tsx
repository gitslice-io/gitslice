import { PageHeader, CommandBlock, NextDocCard } from "./DocParts";
import { startSteps } from "./docData";

export function StartHereDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Start Here"
        description="Use this path if you want to get productive before learning the whole model. Gitslice gives you repository-like slices over one global source graph, and changesets are how work lands."
      />

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          First workflow
        </h2>
        <ol className="mt-4 space-y-5">
          {startSteps.map((step, index) => (
            <li className="flex gap-3 text-sm leading-6" key={step.title}>
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-zinc-950 dark:bg-zinc-100 text-xs font-semibold text-white dark:text-zinc-950">
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h3 className="font-semibold text-zinc-950 dark:text-zinc-50">{step.title}</h3>
                <p className="mt-1 text-slate-600 dark:text-zinc-400">{step.description}</p>
                <CommandBlock>{step.command}</CommandBlock>
              </div>
            </li>
          ))}
        </ol>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Learn the native nouns once the first workflow makes sense."
          section="concepts"
          title="Understand the model"
        />
        <NextDocCard
          description="Coming from Git? Start with the translation table."
          section="git-users"
          title="Read the Git guide"
        />
      </section>
    </div>
  );
}
