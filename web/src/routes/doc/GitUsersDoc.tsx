import {
  CommandBlock,
  DocLink,
  DocSectionCard,
  DocTable,
  InlineCode,
  NextDocCard,
  PageHeader
} from "./DocParts";
import { gitQuestions, gitTranslations } from "./gitUsersData";

export function GitUsersDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="For Git Users"
        description="Start here if your muscle memory is built around repos, branches, local commits, and pull requests. Gitslice keeps familiar boundaries while simplifying the write and review path."
      />

      <DocSectionCard title="Git to Gitslice">
        <DocTable
          ariaLabel="Git to Gitslice terminology mapping"
          head={["Git habit or term", "Gitslice equivalent"]}
          rows={gitTranslations}
        />
      </DocSectionCard>

      <DocSectionCard title="Familiar workflow, native submit">
        <CommandBlock label="daily workflow commands">{`gs auth login
gs init <account>/<slice> ~/work/project
cd ~/work/project
# edit files under <account>/...
gs status
gs diff
gs create --all --message "change title"
gs submit`}</CommandBlock>
        <p>
          The key shift: you do not create local commits or manage feature branches before review.
          Your changeset is the review unit (updated with <InlineCode>gs modify</InlineCode>), and{" "}
          <InlineCode>gs submit</InlineCode> creates the accepted native commit on{" "}
          <InlineCode>refs/global/main</InlineCode>. Need to bring an existing Git repo over first?
          Run{" "}
          <InlineCode>gs import &lt;repo-url&gt; --mount /&lt;account&gt;/&lt;project&gt;</InlineCode>{" "}
          — and optionally attach a <DocLink section="mirrors">Git mirror</DocLink> to keep GitHub
          in sync after every landing.
        </p>
      </DocSectionCard>

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

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Keep a live GitHub backup of any slice with automatic pushes on every landing and tag."
          section="mirrors"
          title="Git Mirrors"
        />
        <NextDocCard
          description="Browse every gs command, install option, and global flag."
          section="cli"
          title="CLI Reference"
        />
      </section>
    </div>
  );
}
