import type { ReactNode } from "react";

import { CommandBlock, NextDocCard, PageHeader, inlineCode } from "./DocParts";

function Section({ children, title }: { children: ReactNode; title: string }) {
  return (
    <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
      <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">{title}</h2>
      <div className="mt-3 space-y-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">{children}</div>
    </section>
  );
}

function Point({ children, label }: { children: ReactNode; label: string }) {
  return (
    <li>
      <span className="font-semibold text-zinc-950 dark:text-zinc-50">{label}</span> {children}
    </li>
  );
}

function Code({ children }: { children: string }) {
  return <code className={inlineCode}>{children}</code>;
}

export function LazyWorkspacesDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Lazy Workspaces"
        description="Mount a slice instead of checking it out. Only the listing is fetched up front; a file's content arrives the first time something reads it, and your edits stay on your machine until you create a changeset. Every gs command works as it does in a checkout."
      />

      <Section title="When to use one">
        <ul className="space-y-2">
          <Point label="Mount (gs init --lazy)">
            when a task touches a part of a large slice: an agent fixing one service, a review, a
            quick edit. The workspace is ready in seconds, and what it costs follows what is read,
            not the size of the slice.
          </Point>
          <Point label="Check out (gs init)">
            when you will read and build nearly everything, need the files with no FUSE (Windows, a
            container without /dev/fuse), or want plain disk speed for every read.
          </Point>
        </ul>
        <p>
          On Ryujinx_Neo (4,508 files, 58.6 MB) a mount was ready in 2.6 s where a full checkout
          took 8 s. The time to mount grows with the number of files, not their size. Benchmarks
          and method:{" "}
          <a
            className="font-medium text-zinc-950 underline decoration-slate-300 underline-offset-4 dark:text-zinc-50"
            href="https://gitslice.io/slices/gitslice/gitslice?path=/gitslice/gitslice/design/28_benchmarks.md"
          >
            design/28_benchmarks.md
          </a>
          .
        </p>
      </Section>

      <Section title="Start">
        <p>Linux or macOS, with FUSE (see Requirements below).</p>
        <CommandBlock>{`gs init --lazy acme/payments ~/work/payments   # mounts; fetches the listing only
cd ~/work/payments
$EDITOR acme/payments/charge.go                 # fetched when opened
gs status                                       # your edits, as in any workspace
gs create --all --message "payments: retry declined charges" --submit`}</CommandBlock>
        <p>
          The mount survives as a workspace: <Code>gs unmount &lt;dir&gt;</Code> stops serving it
          with its edits kept, and <Code>gs mount &lt;dir&gt;</Code> brings it back after an
          unmount or a reboot. <Code>gs mount list</Code> shows every lazy workspace on the
          machine, and <Code>gs mount discard &lt;dir&gt;</Code> deletes an unmounted one with its
          edits. One
          background process serves all of them and exits when the last is unmounted.
        </p>
      </Section>

      <Section title="How files are fetched">
        <ul className="space-y-2">
          <Point label="Listing and metadata:">
            <Code>ls</Code>, <Code>stat</Code> and directory walks are answered from the listing,
            with no network.
          </Point>
          <Point label="First read:">
            a file is fetched the first time something opens it, together with the small files
            beside it (under 64 KiB, in the same request), since reading one file of a folder
            usually means reading more. Each first read is a round trip to the server.
          </Point>
          <Point label="Walks of the tree:">
            when reads follow the tree in order, as <Code>grep -r</Code>, <Code>rg</Code>, a build
            or an indexer do (in either direction), the mount fetches the files ahead of the reader.
            A cold <Code>rg</Code> over Ryujinx_Neo went from 175 s to 14 s with it. Scattered reads
            fetch nothing extra. <Code>GS_READAHEAD=off</Code> turns it off.
          </Point>
          <Point label="Everything, ahead of time:">
            <Code>gs fetch</Code> (or <Code>gs fetch &lt;path&gt;...</Code>) fetches in batches, in
            parallel. It is still the fastest way to prepare for work that reads everything, and it
            is what makes a workspace readable offline.
          </Point>
          <Point label="Known patterns:">
            <Code>{"gs init --lazy <slice> <dir> --prefetch 'acme/app/**/*.go'"}</Code> fetches
            matching files in the background, now and after every sync.
          </Point>
          <Point label="The cache:">
            fetched content is kept in a cache shared by every workspace on the machine, so a second
            mount of the same slice reads what the first fetched.
          </Point>
        </ul>
      </Section>

      <Section title="Where your edits live">
        <p>
          Writes go to the workspace&apos;s local layer, on your machine. Nothing is uploaded until{" "}
          <Code>gs create</Code> or <Code>gs modify</Code>, which upload only what you changed.
          Edit with any tool; <Code>gs status</Code> and <Code>gs diff</Code> read the local layer
          instead of scanning the tree, so they are instant at any size.
        </p>
        <ul className="space-y-2">
          <Point label="Undo:">
            <Code>gs restore &lt;path&gt;...</Code> or <Code>gs restore --all</Code> drops local
            edits, so the slice&apos;s content shows again.
          </Point>
          <Point label="Moving the base:">
            <Code>gs sync</Code> and switching between changesets (<Code>gs switch</Code>,{" "}
            <Code>gs up</Code>, <Code>gs down</Code>) move the mount without rewriting or fetching
            files: files nobody changed take the new base, and new content is fetched when read.
          </Point>
        </ul>
      </Section>

      <Section title="Checkpoints, rewind and fork">
        <p>
          A checkpoint saves the workspace&apos;s edits on this machine, to come back to. Nothing
          is uploaded. Agents use them to try an approach and back out of it cheaply.
        </p>
        <CommandBlock>{`gs checkpoint -m "tests pass, before the refactor"
gs checkpoint list           # newest first, with numbers
gs rewind 3                  # put the edits back as they were at checkpoint 3
gs rewind                    # back to the newest one that differs from now
gs fork 3 ~/work/payments-b  # open checkpoint 3 as a second workspace`}</CommandBlock>
        <ul className="space-y-2">
          <Point label="Automatic:">
            gs takes a checkpoint by itself before <Code>gs sync</Code>, <Code>gs switch</Code>,{" "}
            <Code>gs restore</Code> and <Code>gs rewind</Code> replace your files, keeping the last
            50. Ones you take with <Code>gs checkpoint</Code> are kept until the workspace is
            deleted.
          </Point>
          <Point label="Undoable:">
            a rewind first checkpoints what it replaces, so another rewind undoes it.
          </Point>
          <Point label="Fork:">
            mounts a new lazy workspace showing the checkpoint, on the base it was taken on, so two
            approaches can be worked side by side. A checkpoint from before a sync moved the
            workspace is opened with <Code>gs fork</Code>, not rewound to.
          </Point>
        </ul>
        <p>Checkpoints work in lazy workspaces only, for now.</p>
      </Section>

      <Section title="Requirements">
        <ul className="space-y-2">
          <Point label="Linux:">
            FUSE 3: <Code>/dev/fuse</Code> and <Code>fusermount3</Code> (the <Code>fuse3</Code>{" "}
            package). No root needed.
          </Point>
          <Point label="macOS:">
            <a
              className="font-medium text-zinc-950 underline decoration-slate-300 underline-offset-4 dark:text-zinc-50"
              href="https://macfuse.github.io"
            >
              macFUSE
            </a>
            . After installing it, allow its system extension in System Settings, Privacy &amp;
            Security, and restart; until then a mount waits and then fails with a hint.
          </Point>
          <Point label="Windows:">not supported; use <Code>gs init</Code>.</Point>
        </ul>
      </Section>

      <Section title="When something is wrong">
        <ul className="space-y-2">
          <Point label="gs doctor">
            checks the whole setup, including whether a test mount works on this machine.
          </Point>
          <Point label="gs mount status [dir]">
            shows the workspace&apos;s base, how much was fetched, and the last errors, with the
            path and the reason.
          </Point>
          <Point label="Offline:">
            reads the cache can answer, and every edit, keep working. A read that needs an
            unreachable server fails after a few seconds of retrying, and the next ones fail at once
            for a while, so a build does not hang on each file. Run <Code>gs fetch</Code> beforehand
            to work offline.
          </Point>
          <Point label="A dead mount:">
            if the process serving it was killed, <Code>fusermount3 -u &lt;dir&gt;</Code> (Linux) or{" "}
            <Code>umount &lt;dir&gt;</Code> (macOS) clears it, and <Code>gs mount &lt;dir&gt;</Code>{" "}
            brings the workspace back, edits included.
          </Point>
        </ul>
      </Section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Workspaces, changesets, patchsets and submit, and how they fit together."
          section="concepts"
          title="Understand the model"
        />
        <NextDocCard
          description="Every command, including gs mount, gs fetch and gs checkpoint."
          section="cli"
          title="CLI reference"
        />
      </section>
    </div>
  );
}
