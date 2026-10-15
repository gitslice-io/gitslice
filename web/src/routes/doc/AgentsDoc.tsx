import { PageHeader, CommandBlock, NextDocCard } from "./DocParts";

export function AgentsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Agents"
        description="Bring your own coding agent. Run an agent daemon on your machine and drive it from a slice's Agents page; each conversation works in its own slice workspace, and its edits land as a normal changeset."
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
          and sign in with that email (it must be verified), then click Accept.
          You become a co-owner and the agent keeps its access. From a
          terminal:
        </p>
        <CommandBlock>{`gs claims list
gs claims accept <agent-subject-id>`}</CommandBlock>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">How it works</h2>
        <ul className="mt-4 space-y-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Daemon:</span> you run{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              gs agent start
            </code>{" "}
            in an empty directory. It holds one outbound connection to the server,
            so it works from behind a firewall with no inbound ports.
          </li>
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Runtime:</span> the
            first supported agent is the{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              codex
            </code>{" "}
            CLI, which must be installed and on your PATH.
          </li>
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Conversations:</span>{" "}
            open a slice&apos;s Agents page and start a conversation. Each one runs
            in its own workspace sub-directory bound to that slice, so multiple
            conversations stay isolated.
          </li>
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Changesets:</span> after
            each turn the agent&apos;s edits are captured as a patchset on a
            changeset for the slice — linked back to the exact conversation that
            produced it.
          </li>
        </ul>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Get started</h2>
        <ol className="mt-4 space-y-5">
          {[
            {
              title: "Start a daemon",
              description:
                "Sign in once, then start the daemon in a fresh, empty directory. It stays online while the process runs.",
              command: "gs auth login\nmkdir my-agent && cd my-agent\ngs agent start"
            },
            {
              title: "Open the Agents page",
              description:
                "On any slice you can write, use the Agents link in the header (next to Changesets and Settings) and start a conversation against your online daemon.",
              command: undefined
            },
            {
              title: "Review the result",
              description:
                "As the agent works, its edits land as patchsets. Open the changeset to see each patchset alongside the conversation that produced it, or use the CLI.",
              command: "gs cs conversation <changeset>"
            }
          ].map((step, index) => (
            <li className="flex gap-3 text-sm leading-6" key={step.title}>
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-zinc-950 dark:bg-zinc-100 text-xs font-semibold text-white dark:text-zinc-950">
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h3 className="font-semibold text-zinc-950 dark:text-zinc-50">{step.title}</h3>
                <p className="mt-1 text-slate-600 dark:text-zinc-400">{step.description}</p>
                {step.command ? (
                  <CommandBlock>{step.command}</CommandBlock>
                ) : null}
              </div>
            </li>
          ))}
        </ol>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Look up the gs agent and conversation commands."
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
