import {
  BulletPoint,
  CommandBlock,
  DocLink,
  DocSectionCard,
  InlineCode,
  NextDocCard,
  PageHeader
} from "./DocParts";

const externalLinkClass =
  "font-medium text-zinc-950 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-950 dark:text-zinc-50 dark:decoration-zinc-600 dark:hover:decoration-zinc-50";

export function MirrorsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Git Mirrors"
        description="Automatically push a slice to GitHub or any other Git host as a live backup or public read-only copy. Gitslice pushes after every landing and tag — no workflow or runner to maintain."
      />

      <DocSectionCard title="Set up a mirror">
        <p>
          A mirror is configured on the slice itself, and only the owning account&apos;s owners and
          admins can view or change it. Store a push token as a slice{" "}
          <DocLink section="secrets">secret</DocLink>, then point the mirror at the remote
          repository:
        </p>
        <CommandBlock label="set up Git mirror commands">{`# read the token from stdin so it stays out of shell history
gs slice secret set acme/app GITHUB_MIRROR_TOKEN < token.txt

gs slice mirror set acme/app --url https://github.com/acme/app.git \\
  --token-secret GITHUB_MIRROR_TOKEN

gs slice mirror acme/app          # last push, pending work, last error
gs slice mirror push acme/app     # trigger a push now
gs slice mirror rm acme/app       # stop mirroring`}</CommandBlock>
        <p>
          In the web app, open the slice&apos;s{" "}
          <span className="font-semibold text-zinc-950 dark:text-zinc-50">Settings</span> and use the{" "}
          <span className="font-semibold text-zinc-950 dark:text-zinc-50">Mirror to Git</span>{" "}
          panel below Secrets to set the repository URL, token secret, branch, and username, or to{" "}
          <span className="font-semibold text-zinc-950 dark:text-zinc-50">Push now</span> or{" "}
          <span className="font-semibold text-zinc-950 dark:text-zinc-50">Stop mirroring</span>.
          The first push starts immediately on save. CLI mirror commands require{" "}
          <InlineCode>gs</InlineCode> v0.4.25 or newer (<InlineCode>gs upgrade</InlineCode>).
        </p>
        <ul className="space-y-2">
          <BulletPoint label="Branch:">
            Defaults to <InlineCode>main</InlineCode>; override with{" "}
            <InlineCode>--branch</InlineCode>.
          </BulletPoint>
          <BulletPoint label="Username:">
            Sent alongside the token. Defaults to <InlineCode>x-access-token</InlineCode> (what
            GitHub expects); pass <InlineCode>--username</InlineCode> for GitLab or other Git hosts.
          </BulletPoint>
          <BulletPoint label="One mirror per slice:">
            Running <InlineCode>gs slice mirror set</InlineCode> again updates the existing mirror
            configuration.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Create a GitHub token">
        <ol className="list-decimal space-y-2 pl-5">
          <li>
            Open GitHub&apos;s{" "}
            <a
              className={externalLinkClass}
              href="https://github.com/settings/personal-access-tokens/new"
              rel="noopener noreferrer"
              target="_blank"
            >
              new fine-grained token
            </a>{" "}
            page (Settings → Developer settings → Personal access tokens → Fine-grained tokens).
          </li>
          <li>Set the resource owner to the account or organization that owns the repository.</li>
          <li>
            Under Repository access, choose{" "}
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">
              Only select repositories
            </span>{" "}
            and pick the target repository.
          </li>
          <li>
            Under Repository permissions, set{" "}
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Contents</span> to Read
            and write. Leave all other permissions at No access.
          </li>
          <li>
            Generate the token and store it with <InlineCode>gs slice secret set</InlineCode>. The
            mirror configuration records only the secret&apos;s name, never its value.
          </li>
        </ol>
        <p>
          Pushes authenticated with a fine-grained personal access token trigger GitHub Actions
          normally, so release and CI workflows listening on branch pushes or new tags keep working.
        </p>
      </DocSectionCard>

      <DocSectionCard title="What is pushed">
        <ul className="space-y-2.5">
          <BulletPoint label="Main branch history:">
            One Git commit per landed Gitslice commit on the slice&apos;s default target, preserving
            author, timestamps, and commit message. Each commit includes a{" "}
            <InlineCode>Gitslice-Commit</InlineCode> trailer so the mirror knows exactly where the
            remote stands.
          </BulletPoint>
          <BulletPoint label="Repository root layout:">
            A slice with a single root folder (such as <InlineCode>/acme/app</InlineCode>) is pushed
            with that folder as the Git repository root. A slice spanning multiple root folders is
            pushed with full paths preserved.
          </BulletPoint>
          <BulletPoint label="Tags:">
            New slice tags (<InlineCode>gs tag create</InlineCode>) are pushed once. A tag already
            present on the remote is never overwritten or moved.
          </BulletPoint>
          <BulletPoint label="Existing repositories:">
            An empty remote receives the full slice history. A remote that is already a mirror of
            the slice resumes cleanly from its last mirrored commit.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="When it pushes">
        <ul className="space-y-2.5">
          <BulletPoint label="Automatic push:">
            Runs after every landing that touches a path in the slice and after every new tag,
            typically completing within seconds.
          </BulletPoint>
          <BulletPoint label="Retries:">
            A failed push stays pending and is retried after 1 minute, 5 minutes, 30 minutes, 2
            hours and 6 hours, then every 12 hours. Nothing is lost while waiting — the next
            successful push brings the remote completely up to date.
          </BulletPoint>
          <BulletPoint label="Status:">
            <InlineCode>gs slice mirror</InlineCode> and the Settings panel report the last pushed
            commit, whether a push is pending, and the most recent error.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="It never force-pushes">
        <p>
          The mirror only fast-forwards the remote branch. If someone commits directly to the remote
          or its tree diverges from the slice, the push stops with an explicit error and never
          overwrites remote commits. To recover, decide whether to bring those remote commits into
          Gitslice as a changeset, reset the remote branch to the last mirrored commit, and run{" "}
          <InlineCode>gs slice mirror push</InlineCode>.
        </p>
        <p>
          To prevent accidental writes on the mirror, protect the branch on the host. On GitHub, a
          ruleset blocking force pushes and branch deletion still allows the mirror&apos;s
          fast-forward pushes through.
        </p>
      </DocSectionCard>

      <DocSectionCard title="When the token expires">
        <p>
          Expired or revoked tokens produce an authentication error in the mirror status. Generate a
          new token and overwrite the same secret name — the next retry or{" "}
          <InlineCode>gs slice mirror push</InlineCode> picks it up automatically:
        </p>
        <CommandBlock label="rotate mirror token commands">{`gs slice secret set acme/app GITHUB_MIRROR_TOKEN < new-token.txt
gs slice mirror push acme/app`}</CommandBlock>
      </DocSectionCard>

      <DocSectionCard title="Security">
        <ul className="space-y-2.5">
          <BulletPoint label="Public HTTPS targets only:">
            Only <InlineCode>https://</InlineCode> URLs resolving to public internet addresses are
            accepted. The server validates the target IP before dialing and pins Git to the checked
            address to block DNS rebinding, and HTTP redirects are refused.
          </BulletPoint>
          <BulletPoint label="Credential hygiene:">
            The push token is passed to Git as an HTTP header via environment variables — never on
            the command line — and scrubbed from any stored or displayed error message.
          </BulletPoint>
          <BulletPoint label="Stored outside the source tree:">
            Mirror URLs and token secret names live in slice settings rather than committed files,
            so a changeset cannot redirect backups.
          </BulletPoint>
          <BulletPoint label="Visibility check:">
            A mirror exports the exact tree a reader of the slice can clone. Confirm the destination
            repository&apos;s visibility before mirroring a private slice.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Current limitations">
        <ul className="list-disc space-y-2 pl-5">
          <li>One mirror per slice, tracking the slice&apos;s default target branch only.</li>
          <li>SSH remotes and GitHub App installations are not supported yet.</li>
          <li>Mirroring is one-way; commits made directly on the remote are not synced back automatically.</li>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Example: Gitslice itself">
        <p>
          The public <InlineCode>gitslice/gitslice</InlineCode> slice mirrors itself this way to{" "}
          <a
            className={externalLinkClass}
            href="https://github.com/gitslice-io/gitslice"
            rel="noopener noreferrer"
            target="_blank"
          >
            github.com/gitslice-io/gitslice
          </a>
          . Every landing arrives on GitHub within seconds, and pushing a{" "}
          <InlineCode>v*</InlineCode> tag triggers the GitHub release workflow automatically.
        </p>
      </DocSectionCard>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Store and manage encrypted push tokens and folder-scoped secrets."
          section="secrets"
          title="Secrets"
        />
        <NextDocCard
          description="Clone slices with Git, import existing repos, and publish Go modules."
          section="git-users"
          title="For Git Users"
        />
      </section>
    </div>
  );
}
