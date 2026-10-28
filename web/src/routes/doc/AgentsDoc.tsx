import { PageHeader, CommandBlock, NextDocCard } from "./DocParts";

export function AgentsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Agents"
        description="Coding agents work in Gitslice through the gs CLI, like a person at a terminal. An agent can sign itself up, and you can claim it, without a browser."
      />

      <section
        className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5"
        id="agent-signup"
      >
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          Let an agent sign itself up
        </h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          An agent can create its own account without a browser or a human. It
          gets a home slice and a long-lived API key saved as its CLI
          credential. It names your email, so you can later claim
          co-ownership of everything it creates. Point an agent at{" "}
          <a
            className="font-medium text-zinc-950 underline underline-offset-2 dark:text-zinc-50"
            href="/llms.txt"
          >
            gitslice.io/llms.txt
          </a>{" "}
          for the full walkthrough.
        </p>
        <CommandBlock>{`curl -fsSL https://gitslice.io/install.sh | sh
gs auth register-agent --username release-bot --email you@example.com
gs auth status`}</CommandBlock>
        <p className="mt-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Usernames are 4-63 lowercase letters, digits and &apos;-&apos;, and
          must be unique. If registration times out, re-run the same command:
          it resumes safely. To claim the agent, open{" "}
          <a
            className="font-medium text-zinc-950 underline underline-offset-2 dark:text-zinc-50"
            href="/claims"
          >
            gitslice.io/claims
          </a>{" "}
          and sign in with an account whose verified email is exactly that
          address, then click Accept. You become a co-owner and the agent keeps
          its access. If your account uses another email, add and verify the
          agent&apos;s one in your profile first (Manage account); a claim never
          appears for a different address. An empty list names the emails it
          looked for. From a terminal:
        </p>
        <CommandBlock>{`gs claims list
gs claims accept <agent-subject-id>`}</CommandBlock>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Run checks on your machine</h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          An agent daemon on any machine with Docker runs a slice&apos;s required
          checks and reports them back; the server enforces them at submit. See{" "}
          <a
            className="font-medium text-zinc-950 underline underline-offset-2 dark:text-zinc-50"
            href="/doc/checks"
          >
            CI Checks
          </a>
          .
        </p>
        <CommandBlock>{`mkdir my-ci && cd my-ci
gs agent start --ci`}</CommandBlock>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Look up the gs agent and auth commands."
          section="cli"
          title="CLI reference"
        />
        <NextDocCard
          description="Understand slices, workspaces, and changesets first."
          section="concepts"
          title="Core concepts"
        />
      </section>
    </div>
  );
}
