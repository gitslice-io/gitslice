import { CommandBlock, PageHeader, inlineCode } from "./DocParts";

const card = "mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5";
const h2 = "text-base font-semibold text-zinc-950 dark:text-zinc-50";
const p = "mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400";
const list = "mt-4 space-y-3 text-sm leading-6 text-slate-600 dark:text-zinc-400";
const strong = "font-semibold text-zinc-950 dark:text-zinc-50";

export function MirrorsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Git Mirrors"
        description="Keep a copy of a slice in a GitHub repository, or any Git host, as a backup or for people who read code there. The server pushes after every landing and every tag; there is no workflow or worker to run."
      />

      <section className={card}>
        <h2 className={h2}>Set up a mirror</h2>
        <p className={p}>
          A mirror is a setting of the slice, and only the slice account&apos;s owners and admins can see or
          change it. First store a token as a secret of the slice, then point the mirror at the repository:
        </p>
        <CommandBlock>{`# the token is read from stdin, so it stays out of your shell history
gs slice secret set acme/app GITHUB_MIRROR_TOKEN < token.txt

gs slice mirror set acme/app --url https://github.com/acme/app.git \\
  --token-secret GITHUB_MIRROR_TOKEN

gs slice mirror acme/app          # last push, anything pending, last error
gs slice mirror push acme/app     # push now
gs slice mirror rm acme/app       # stop mirroring`}</CommandBlock>
        <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          In the web app, open the slice&apos;s <span className={strong}>Settings</span> and use the{" "}
          <span className={strong}>Mirror to Git</span> panel, under Secrets: the repository URL, the token
          secret, the branch and the user name, with <span className={strong}>Push now</span> and{" "}
          <span className={strong}>Stop mirroring</span>. The first push starts as soon as you save. The{" "}
          <code className={inlineCode}>gs slice mirror</code> commands need <code className={inlineCode}>gs</code>{" "}
          v0.4.25 or later.
        </p>
        <ul className={list}>
          <li>
            <span className={strong}>Branch:</span> <code className={inlineCode}>main</code> by default; change it
            with <code className={inlineCode}>--branch</code>.
          </li>
          <li>
            <span className={strong}>User name:</span> sent with the token,{" "}
            <code className={inlineCode}>x-access-token</code> by default, which is what GitHub expects. For
            GitLab and other hosts, pass the user name they expect with <code className={inlineCode}>--username</code>.
          </li>
          <li>
            <span className={strong}>One mirror per slice:</span> running{" "}
            <code className={inlineCode}>gs slice mirror set</code> again changes it.
          </li>
        </ul>
      </section>

      <section className={card}>
        <h2 className={h2}>Create a GitHub token</h2>
        <ol className="mt-4 list-decimal space-y-2 pl-5 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          <li>
            Open GitHub&apos;s{" "}
            <a
              className="font-medium text-zinc-950 underline dark:text-zinc-50"
              href="https://github.com/settings/personal-access-tokens/new"
            >
              new fine-grained token
            </a>{" "}
            page (Settings, Developer settings, Personal access tokens, Fine-grained tokens).
          </li>
          <li>Set the resource owner to the account or organization that owns the repository.</li>
          <li>Under Repository access, choose Only select repositories and pick the one repository.</li>
          <li>
            Under Repository permissions, set <span className={strong}>Contents</span> to Read and write. Leave
            everything else at No access.
          </li>
          <li>
            Generate it and store it with <code className={inlineCode}>gs slice secret set</code>. The mirror
            setting keeps only the secret&apos;s name, never its value.
          </li>
        </ol>
        <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Pushes made with a personal token start GitHub Actions workflows, so workflows that run on a push to the
          branch or on a new tag keep working.
        </p>
      </section>

      <section className={card}>
        <h2 className={h2}>What is pushed</h2>
        <ul className={list}>
          <li>
            <span className={strong}>The main branch:</span> the history of the slice&apos;s default target, one Git
            commit per landed commit, with its author, dates and message. Each commit carries a{" "}
            <code className={inlineCode}>Gitslice-Commit</code> trailer, which is how the mirror finds where the
            remote stands.
          </li>
          <li>
            <span className={strong}>The root folder:</span> a slice with one root folder, such as{" "}
            <code className={inlineCode}>/acme/app</code>, is pushed with that folder as the repository root. A
            slice of several folders is pushed with full paths.
          </li>
          <li>
            <span className={strong}>Tags:</span> new tags of the slice are pushed once. A tag the remote already
            has is never moved.
          </li>
          <li>
            <span className={strong}>An existing repository:</span> an empty one receives the whole history. One
            that is already a mirror of the slice is picked up where it stands.
          </li>
        </ul>
      </section>

      <section className={card}>
        <h2 className={h2}>When it pushes</h2>
        <ul className={list}>
          <li>
            <span className={strong}>After every landing</span> that changes a path in the slice, and after every
            new tag, usually within seconds.
          </li>
          <li>
            <span className={strong}>Retries:</span> a failed push stays pending and is tried again after 1 minute,
            5 minutes, 30 minutes, 2 hours and 6 hours, then every 12 hours. Nothing is lost while it waits: the
            next good push brings the remote fully up to date.
          </li>
          <li>
            <span className={strong}>Status:</span> <code className={inlineCode}>gs slice mirror</code> and the
            settings panel show the last pushed commit, whether a push is pending, and the last error.
          </li>
        </ul>
      </section>

      <section className={card}>
        <h2 className={h2}>It never force-pushes</h2>
        <p className={p}>
          The mirror only moves the remote branch forward. If someone commits to the remote directly, or its files
          differ from the slice, the push stops with an error saying so, and nothing on the remote is overwritten.
          To recover, decide what to do with those commits (bring them into Gitslice as a changeset if you want to
          keep them), reset the remote branch to the last mirrored commit, then run{" "}
          <code className={inlineCode}>gs slice mirror push</code>.
        </p>
        <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          To stop people from pushing to the mirror, protect the branch on the host. On GitHub, a ruleset that
          blocks force pushes and deletion still lets the mirror&apos;s fast-forward pushes through.
        </p>
      </section>

      <section className={card}>
        <h2 className={h2}>When the token expires</h2>
        <p className={p}>
          Pushes fail with an authentication error, shown in the status. Create a new token and store it under the
          same secret name; the next retry, or <code className={inlineCode}>gs slice mirror push</code>, uses it:
        </p>
        <CommandBlock>{`gs slice secret set acme/app GITHUB_MIRROR_TOKEN < new-token.txt
gs slice mirror push acme/app`}</CommandBlock>
      </section>

      <section className={card}>
        <h2 className={h2}>Security</h2>
        <ul className={list}>
          <li>
            <span className={strong}>Targets:</span> <code className={inlineCode}>https://</code> URLs on public
            addresses only. The host is checked before each push and Git is pinned to the checked address, so a DNS
            change cannot send the push elsewhere. Redirects are refused.
          </li>
          <li>
            <span className={strong}>The token</span> reaches Git as an HTTP header through its environment, never
            on a command line, and is removed from any error before it is stored or shown.
          </li>
          <li>
            <span className={strong}>Outside the tree:</span> the URL and token are slice settings, not files, so a
            changeset cannot redirect the backup.
          </li>
          <li>
            <span className={strong}>What leaves:</span> the mirror pushes what a reader of the slice could already
            clone. Check the remote repository&apos;s visibility before mirroring a private slice.
          </li>
        </ul>
      </section>

      <section className={card}>
        <h2 className={h2}>Not supported yet</h2>
        <ul className="mt-4 list-disc space-y-2 pl-5 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          <li>Several mirrors per slice, or branches other than the default target.</li>
          <li>SSH remotes and GitHub App installations.</li>
          <li>Bringing changes made on the mirror back into Gitslice automatically.</li>
        </ul>
      </section>

      <section className={card}>
        <h2 className={h2}>Example: Gitslice itself</h2>
        <p className={p}>
          The <code className={inlineCode}>gitslice/gitslice</code> slice is mirrored this way to{" "}
          <a className="font-medium text-zinc-950 underline dark:text-zinc-50" href="https://github.com/gitslice-io/gitslice">
            github.com/gitslice-io/gitslice
          </a>
          . Every landing reaches GitHub within seconds, and the release workflow there starts when a{" "}
          <code className={inlineCode}>v*</code> tag arrives.
        </p>
      </section>
    </div>
  );
}
