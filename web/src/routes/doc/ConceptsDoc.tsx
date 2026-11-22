import {
  BulletPoint,
  CommandBlock,
  DocSectionCard,
  DocTable,
  InlineCode,
  NextDocCard,
  PageHeader
} from "./DocParts";
import { conceptCards, lifecycleSteps, sliceRoles } from "./conceptsData";

export function ConceptsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Concepts"
        description="Gitslice is native source storage first, Git compatibility second. These core nouns explain how paths, slices, workspaces, permissions, and landings fit together."
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

      <DocSectionCard title="How the pieces line up">
        <p>
          One mental model covers every command: global paths live under an account, a slice picks
          the paths a person or agent needs, and a workspace checks out or mounts only that slice.
        </p>
        <CommandBlock label="object hierarchy example">{`Global source graph:  /acme/services/payments/...
                      /acme/libs/money/...

Slice acme/payments:  includes /acme/services/payments and /acme/libs/money
Workspace:            ~/work/payments/
                        acme/services/payments/charge.go
                        acme/libs/money/currency.go
Changeset:            edits inside acme/payments -> patchset 1, 2, ... -> native commit on refs/global/main`}</CommandBlock>
      </DocSectionCard>

      <DocSectionCard title="Submit lifecycle">
        <div className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
          {lifecycleSteps.map(([title, description]) => (
            <div
              className="rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 p-4"
              key={title}
            >
              <h3 className="font-semibold text-zinc-950 dark:text-zinc-50">{title}</h3>
              <p className="mt-2 leading-6 text-slate-600 dark:text-zinc-400">{description}</p>
            </div>
          ))}
        </div>
        <ul className="mt-2 space-y-2">
          <BulletPoint label="Moving bases:">
            By default, if <InlineCode>refs/global/main</InlineCode> advances while a changeset is
            open, run <InlineCode>gs sync</InlineCode> before submitting. Enable{" "}
            <InlineCode>--merge-on-submit on</InlineCode> on a slice to let the server merge
            non-overlapping stale changesets automatically as a new patchset (re-running checks and
            approvals).
          </BulletPoint>
          <BulletPoint label="Advisory path intents:">
            When multiple people or agents edit the same slice in parallel, reserve a path or glob
            with <InlineCode>gs intent &apos;src/billing/**&apos; --for 2h -m &quot;refactor billing&quot;</InlineCode>{" "}
            so overlapping work surfaces warnings in <InlineCode>gs status</InlineCode>,{" "}
            <InlineCode>gs sync</InlineCode>, and <InlineCode>gs create</InlineCode>.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Slice roles and approver groups">
        <p>
          Share any slice with a user, agent, or account group (<InlineCode>@account/name</InlineCode>
          ) without adding them to the owning account:
        </p>
        <DocTable
          ariaLabel="Slice roles and permissions"
          firstColMono
          head={["Role", "Permissions"]}
          rows={sliceRoles}
        />
        <p>
          Slice owners can also require distinct non-author approvals (
          <InlineCode>--required-approvals</InlineCode>), distinct human approvals (
          <InlineCode>--required-human-approvals</InlineCode>), or restrict eligible approvers to
          an account group (<InlineCode>--approvers @acme/reviewers</InlineCode>).
        </p>
      </DocSectionCard>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Mount large slices on demand and use local checkpoints and forks."
          section="lazy"
          title="Lazy Workspaces"
        />
        <NextDocCard
          description="Define folder-scoped CI checks that run on every patchset and gate submit."
          section="checks"
          title="Set up CI Checks"
        />
      </section>
    </div>
  );
}
