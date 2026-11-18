import {
  CommandBlock,
  DocSectionCard,
  DocTable,
  InlineCode,
  NextDocCard,
  PageHeader
} from "./DocParts";
import { commandGroups, globalFlags } from "./cliReferenceData";

const externalLinkClass =
  "font-medium text-zinc-950 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-950 dark:text-zinc-50 dark:decoration-zinc-600 dark:hover:decoration-zinc-50";

export function CliReferenceDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="CLI Reference"
        description="Install options, global flags, and a compact command lookup for daily gs work. CLI commands accept account/slice and account:slice interchangeably."
      />

      <nav
        aria-label="CLI reference sections"
        className="mt-6 flex flex-wrap gap-2 text-xs"
      >
        {[
          ["install", "Install"],
          ["global-flags", "Global flags"],
          ...commandGroups.map((group) => [group.id, group.title] as [string, string])
        ].map(([id, label]) => (
          <a
            className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2.5 py-1 font-medium text-slate-700 dark:text-zinc-300 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50"
            href={`#${id}`}
            key={id}
          >
            {label}
          </a>
        ))}
      </nav>

      <DocSectionCard id="install" title="Install the gs CLI">
        <p>
          The CLI is a single Go binary named <InlineCode>gs</InlineCode>. Prebuilt binaries need no
          runtime dependencies; building from source requires Go 1.24 or newer.
        </p>

        <h3 className="pt-2 text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          Option A — prebuilt binary (quickest)
        </h3>
        <CommandBlock label="install script command">{`curl -fsSL https://gitslice.io/install.sh | sh`}</CommandBlock>
        <p>
          Downloads the latest release for Linux, macOS, or Windows POSIX shells such as Git Bash
          (amd64 or arm64), verifies its SHA-256 checksum, and installs to{" "}
          <InlineCode>~/.local/bin</InlineCode> (override with{" "}
          <InlineCode>GS_INSTALL_DIR</InlineCode>; set <InlineCode>GS_FROM_SOURCE=1</InlineCode> to
          build with Go instead). Standalone Windows and other archives are on the{" "}
          <a
            className={externalLinkClass}
            href="https://gitslice.io/releases/latest"
            rel="noopener noreferrer"
            target="_blank"
          >
            releases page
          </a>
          . If <InlineCode>gs</InlineCode> is not found after installing, add{" "}
          <InlineCode>~/.local/bin</InlineCode> to <InlineCode>PATH</InlineCode>.
        </p>

        <p>
          To inspect and verify the install script before running it: each release ships its own
          checksummed copy matching the public Git mirror at that release tag.
        </p>
        <CommandBlock label="verify install script commands">{`curl -fsSLO https://gitslice.io/releases/latest/download/install.sh
curl -fsSL https://gitslice.io/releases/latest/download/checksums.txt | grep ' install.sh$' | sha256sum -c -
tag=$(curl -fsSI https://gitslice.io/releases/latest | sed -n 's#^[Ll]ocation: .*/tag/##p' | tr -d '\\r')
git clone -q --depth 1 --branch "$tag" https://gitslice.io/git/gitslice/gitslice.git gs-src && cmp gs-src/gitslice/gitslice/web/public/install.sh install.sh
less install.sh && sh install.sh`}</CommandBlock>

        <h3 className="pt-2 text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          Option B — go install
        </h3>
        <CommandBlock label="go install command">{`go install gitslice.io/gitslice/cmd/gs@latest`}</CommandBlock>
        <p>
          Make sure your Go bin directory is on <InlineCode>PATH</InlineCode>:
        </p>
        <CommandBlock label="Go PATH export command">{`export PATH="$PATH:$(go env GOPATH)/bin"`}</CommandBlock>

        <h3 className="pt-2 text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          Option C — build from source
        </h3>
        <CommandBlock label="build from source commands">{`git clone https://gitslice.io/git/gitslice/gitslice.git gitslice-src
cd gitslice-src/gitslice/gitslice
make install   # builds gs (and gitslice-server) into your Go bin`}</CommandBlock>
        <p>
          Gitslice hosts its own source in the public slice{" "}
          <a
            className={externalLinkClass}
            href="https://gitslice.io/slices/gitslice/gitslice"
          >
            gitslice/gitslice
          </a>
          . Clones preserve the account-rooted layout, so the Go module sits under{" "}
          <InlineCode>gitslice/gitslice/</InlineCode>.
        </p>

        <h3 className="pt-2 text-sm font-semibold text-zinc-950 dark:text-zinc-50">Verify</h3>
        <p>
          <InlineCode>gs</InlineCode> defaults to the hosted endpoint (
          <InlineCode>api.gitslice.io:443</InlineCode>), so you can sign in immediately. Point at
          another deployment with <InlineCode>--server</InlineCode> or{" "}
          <InlineCode>GS_SERVER_ADDR</InlineCode>.
        </p>
        <CommandBlock label="verify CLI commands">{`gs version
gs auth login
gs auth status
gs doctor`}</CommandBlock>
      </DocSectionCard>

      <DocSectionCard id="global-flags" title="Global flags">
        <p>
          These flags work across <InlineCode>gs</InlineCode> commands and are especially useful in
          scripts and agent workflows:
        </p>
        <DocTable
          ariaLabel="Global CLI flags"
          firstColMono
          head={["Flag", "Effect"]}
          rows={globalFlags}
        />
      </DocSectionCard>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        {commandGroups.map((group) => (
          <article
            className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5"
            id={group.id}
            key={group.title}
          >
            <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
              {group.title}
            </h2>
            <dl className="mt-4 space-y-3">
              {group.commands.map(([command, description]) => (
                <div key={command}>
                  <dt>
                    <code className="rounded-md bg-slate-50 dark:bg-zinc-950 px-2 py-1 font-mono text-xs text-slate-700 dark:text-zinc-300">
                      {command}
                    </code>
                  </dt>
                  <dd className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
                    {description}
                  </dd>
                </div>
              ))}
            </dl>
          </article>
        ))}
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Walk through your first slice checkout, edit, changeset, and submit."
          section="start"
          title="Start Here"
        />
        <NextDocCard
          description="Set up coding agents with self-signup, delegated keys, contributor roles, and MCP."
          section="agents"
          title="Agents"
        />
      </section>
    </div>
  );
}
