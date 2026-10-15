import { PageHeader, CommandBlock } from "./DocParts";
import { commandGroups } from "./docData";

export function CliReferenceDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="CLI Reference"
        description="A compact lookup for the commands used in normal Gitslice work. Use Start Here for the walkthrough."
      />

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          Install the gs CLI
        </h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          The CLI is a single Go binary named{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            gs
          </code>
          . Prebuilt binaries need nothing else; building it yourself requires
          Go 1.24 or newer.
        </p>

        <h3 className="mt-5 text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          Option A — prebuilt binary (quickest)
        </h3>
        <CommandBlock>{`curl -fsSL https://gitslice.io/install.sh | sh`}</CommandBlock>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Downloads the latest release for Linux or macOS (amd64 or arm64),
          verifies its checksum, and installs it to{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            ~/.local/bin
          </code>
          . Windows and other builds are on the{" "}
          <a
            className="font-medium text-zinc-950 underline underline-offset-2 dark:text-zinc-50"
            href="https://gitslice.io/releases/latest"
          >
            releases page
          </a>
          .
        </p>

        <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          To read and check the script before running it: each release carries
          its own copy, listed in that release&apos;s checksums, and the same
          file is in the public GitHub mirror at the release tag.
        </p>
        <CommandBlock>{`curl -fsSLO https://gitslice.io/releases/latest/download/install.sh
curl -fsSL https://gitslice.io/releases/latest/download/checksums.txt | grep ' install.sh$' | sha256sum -c -
tag=$(curl -fsSI https://gitslice.io/releases/latest | sed -n 's#^[Ll]ocation: .*/tag/##p' | tr -d '\\r')
curl -fsSL "https://raw.githubusercontent.com/gitslice-io/gitslice/$tag/web/public/install.sh" | cmp - install.sh
less install.sh && sh install.sh`}</CommandBlock>

        <h3 className="mt-5 text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          Or with Homebrew (macOS and Linux)
        </h3>
        <CommandBlock>{`brew install gitslice-io/tap/gitslice`}</CommandBlock>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Installs gs with its shell completions. Upgrade it with{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            brew upgrade gitslice
          </code>
          . Ghostscript also installs a gs command, so the two cannot be
          installed together with Homebrew.
        </p>

        <h3 className="mt-5 text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          Option B — go install
        </h3>
        <CommandBlock>{`go install gitslice.io/gitslice/cmd/gs@latest`}</CommandBlock>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Make sure your Go bin directory is on{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            PATH
          </code>
          :
        </p>
        <CommandBlock>{`export PATH="$PATH:$(go env GOPATH)/bin"`}</CommandBlock>

        <h3 className="mt-5 text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          Option C — build from source
        </h3>
        <CommandBlock>{`git clone https://gitslice.io/git/gitslice/gitslice.git gitslice-src
cd gitslice-src/gitslice/gitslice
make install   # builds gs (and gitslice-server) into your Go bin`}</CommandBlock>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Gitslice hosts its own source in the public slice{" "}
          <a
            className="font-medium text-zinc-950 underline underline-offset-2 dark:text-zinc-50"
            href="https://gitslice.io/slices/gitslice/gitslice"
          >
            gitslice/gitslice
          </a>
          . Clones keep the account-rooted layout, so the module sits under{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            gitslice/gitslice/
          </code>
          .
        </p>

        <h3 className="mt-5 text-sm font-semibold text-zinc-950 dark:text-zinc-50">Verify</h3>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            gs
          </code>{" "}
          defaults to the hosted endpoint, so you can sign in right away. Point
          at a different server with{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            --server
          </code>{" "}
          or{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            GS_SERVER_ADDR
          </code>
          .
        </p>
        <CommandBlock>{`gs version
gs auth login
gs auth status`}</CommandBlock>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        {commandGroups.map((group) => (
          <article
            className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5"
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
    </div>
  );
}
