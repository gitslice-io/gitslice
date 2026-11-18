import {
  BulletPoint,
  CommandBlock,
  DocLink,
  DocSectionCard,
  InlineCode,
  NextDocCard,
  PageHeader
} from "./DocParts";
import { startSteps } from "./startHereData";

export function StartHereDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Start Here"
        description="Get productive in five minutes. Gitslice gives you repository-like slices over a single global source graph, and changesets are how work is reviewed and landed."
      />

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          First workflow
        </h2>
        <ol className="mt-4 space-y-6">
          {startSteps.map((step, index) => (
            <li className="flex gap-3 text-sm leading-6" key={step.title}>
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-zinc-950 dark:bg-zinc-100 text-xs font-semibold text-white dark:text-zinc-950">
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h3 className="font-semibold text-zinc-950 dark:text-zinc-50">{step.title}</h3>
                <p className="mt-1 text-slate-600 dark:text-zinc-400">{step.description}</p>
                <CommandBlock label={step.label}>{step.command}</CommandBlock>
                {step.note ? (
                  <p className="mt-2 text-xs leading-5 text-slate-500 dark:text-zinc-400">
                    {step.note}
                  </p>
                ) : null}
              </div>
            </li>
          ))}
        </ol>
      </section>

      <DocSectionCard title="Good to know up front">
        <ul className="space-y-2">
          <BulletPoint label="Private by default:">
            New slices — including your home slice — are visible only to signed-in members of your
            account. Share a slice with teammates or agents via{" "}
            <InlineCode>gs slice share</InlineCode>, or make it publicly readable with{" "}
            <InlineCode>gs slice update &lt;account&gt;/&lt;slice&gt; --visibility public</InlineCode>.
          </BulletPoint>
          <BulletPoint label="Large slices:">
            Use <InlineCode>gs init --lazy</InlineCode> on Linux or macOS with FUSE to mount a
            slice in seconds and fetch file contents on demand. See{" "}
            <DocLink section="lazy">Lazy Workspaces</DocLink>.
          </BulletPoint>
          <BulletPoint label="Other install paths:">
            Need Windows binaries, <InlineCode>go install</InlineCode>, or a verified source build?
            See the <DocLink section="cli">CLI Reference</DocLink>.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <section className="mt-8 grid gap-4 md:grid-cols-3">
        <NextDocCard
          description="Learn how accounts, slices, workspaces, changesets, and roles fit together."
          section="concepts"
          title="Understand the model"
        />
        <NextDocCard
          description="Coming from Git? Translate repos, branches, PRs, and imports."
          section="git-users"
          title="Read the Git guide"
        />
        <NextDocCard
          description="Set up coding agents with self-signup, contributor roles, or MCP."
          section="agents"
          title="Work with agents"
        />
      </section>
    </div>
  );
}
