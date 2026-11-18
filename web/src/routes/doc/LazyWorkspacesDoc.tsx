import { Link } from "@tanstack/react-router";

import {
  BulletPoint,
  CommandBlock,
  DocSectionCard,
  InlineCode,
  NextDocCard,
  PageHeader
} from "./DocParts";

const linkClass =
  "font-medium text-zinc-950 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-950 dark:text-zinc-50 dark:decoration-zinc-600 dark:hover:decoration-zinc-50";

export function LazyWorkspacesDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Lazy Workspaces"
        description="Mount a slice instead of checking every file out up front. Only the directory listing is downloaded at mount time; file contents arrive on first read, and local edits stay on your machine until you create a changeset."
      />

      <DocSectionCard title="When to use one">
        <ul className="space-y-2">
          <BulletPoint label="Mount (gs init --lazy):">
            Best when a task touches a small part of a large slice — an agent fixing one service, a
            code review, or a quick edit. The workspace is ready in seconds, and bandwidth scales
            with the files you actually read rather than the size of the slice.
          </BulletPoint>
          <BulletPoint label="Full checkout (gs init):">
            Best when you will read and compile nearly everything, need a workspace without FUSE
            (Windows or a container without <InlineCode>/dev/fuse</InlineCode>), or want raw local
            disk speed on every read.
          </BulletPoint>
        </ul>
        <p>
          On a 4,508-file, 58.6 MB repository (Ryujinx_Neo), a cold lazy mount was ready in 2.6 s
          where a full checkout took 8 s; mount time scales with file count rather than file size.
          Read the full write-up in{" "}
          <Link
            className={linkClass}
            params={{ slug: "benchmarking-gitslice-against-git" }}
            to="/blogs/$slug"
          >
            Benchmarking Gitslice Against Git
          </Link>{" "}
          and{" "}
          <Link
            className={linkClass}
            params={{ slug: "what-we-learned-from-abfs" }}
            to="/blogs/$slug"
          >
            What We Learned from Google&apos;s ABFS
          </Link>
          .
        </p>
      </DocSectionCard>

      <DocSectionCard title="Start">
        <p>
          Requires Linux or macOS with FUSE (see Requirements below). Inside the mount, files sit at
          their full account-rooted path (for example, <InlineCode>acme/payments/charge.go</InlineCode>
          ). Pass <InlineCode>--read-only</InlineCode> for inspection or review mounts where writes
          should be refused.
        </p>
        <CommandBlock label="lazy workspace quickstart commands">{`gs init --lazy acme/payments ~/work/payments   # mounts; fetches the listing only
cd ~/work/payments
$EDITOR acme/payments/charge.go                 # fetched on first open
gs status                                       # shows local edits, like any workspace
gs create --all --message "payments: retry declined charges" --submit`}</CommandBlock>
        <p>
          A lazy mount persists as a normal workspace: <InlineCode>gs unmount &lt;dir&gt;</InlineCode>{" "}
          stops serving it while keeping local edits, and{" "}
          <InlineCode>gs mount &lt;dir&gt;</InlineCode> brings it back after an unmount or reboot.{" "}
          <InlineCode>gs mount list</InlineCode> lists every lazy workspace on this machine,{" "}
          <InlineCode>gs workspace delete &lt;dir&gt;</InlineCode> cleanly unmounts and removes one
          (refusing if edits are not yet in a changeset unless <InlineCode>--force</InlineCode> is
          passed), and <InlineCode>gs mount discard &lt;dir&gt;</InlineCode> deletes an unmounted
          workspace with its edits. One background daemon serves all your lazy mounts and exits when
          the last one is unmounted.
        </p>
      </DocSectionCard>

      <DocSectionCard title="How files are fetched">
        <ul className="space-y-2">
          <BulletPoint label="Listing and metadata:">
            <InlineCode>ls</InlineCode>, <InlineCode>stat</InlineCode>, and directory walks are
            answered from the cached listing with no network round trip.
          </BulletPoint>
          <BulletPoint label="First read and sibling batching:">
            Opening an uncached file fetches it from the server together with small sibling files in
            the same folder (under 64 KiB, in the same request), since reading one file in a
            directory usually means reading its neighbors too.
          </BulletPoint>
          <BulletPoint label="Sequential readahead:">
            When reads walk the tree in order — as <InlineCode>grep -r</InlineCode>,{" "}
            <InlineCode>rg</InlineCode>, a compiler, or an indexer do (in either direction) — the
            mount fetches upcoming files ahead of the reader. A cold <InlineCode>rg</InlineCode>{" "}
            over Ryujinx_Neo dropped from 175 s to 14 s with readahead enabled; scattered reads
            fetch nothing extra. Set <InlineCode>GS_READAHEAD=off</InlineCode> to disable it.
          </BulletPoint>
          <BulletPoint label="Bulk prefetch for full builds or offline work:">
            <InlineCode>gs fetch</InlineCode> (or{" "}
            <InlineCode>gs fetch &lt;path&gt;...</InlineCode>) downloads files in parallel batches.
            It is the fastest way to warm a mount before reading everything and makes the workspace
            usable offline.
          </BulletPoint>
          <BulletPoint label="Background patterns:">
            <InlineCode>{"gs init --lazy <slice> <dir> --prefetch 'acme/app/**/*.go'"}</InlineCode>{" "}
            warms matching files in the background immediately and after every{" "}
            <InlineCode>gs sync</InlineCode>.
          </BulletPoint>
          <BulletPoint label="Shared machine cache:">
            Fetched blobs live in <InlineCode>~/.cache/gitslice</InlineCode> and are shared across
            every workspace on the machine, so a second mount of the same slice reads what the first
            already fetched. Listings are cached too, so newer checkouts, mounts, and{" "}
            <InlineCode>gs sync</InlineCode> download only what changed. Once the cache exceeds{" "}
            <InlineCode>GS_CACHE_MAX</InlineCode> (default <InlineCode>10G</InlineCode>;{" "}
            <InlineCode>off</InlineCode> disables pruning), least-recently-used blobs are evicted
            while preserving checkpoints, sync conflicts, and anything used in the last six hours.
            Inspect with <InlineCode>gs cache status</InlineCode> or prune immediately with{" "}
            <InlineCode>gs cache prune</InlineCode>.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Where your edits live">
        <p>
          Writes stay in the workspace&apos;s local overlay on disk. Nothing is uploaded until you
          run <InlineCode>gs create</InlineCode> or <InlineCode>gs modify</InlineCode>, which send
          only the paths you changed. Because <InlineCode>gs status</InlineCode> and{" "}
          <InlineCode>gs diff</InlineCode> read the local overlay rather than scanning the entire
          tree, they stay instant at any slice size.
        </p>
        <ul className="space-y-2">
          <BulletPoint label="Discard local edits:">
            <InlineCode>gs restore &lt;path&gt;...</InlineCode> or{" "}
            <InlineCode>gs restore --all</InlineCode> drops local writes so the slice&apos;s base
            content shows again.
          </BulletPoint>
          <BulletPoint label="Move the base without re-downloading:">
            <InlineCode>gs sync</InlineCode> and changeset switching (
            <InlineCode>gs switch</InlineCode>, <InlineCode>gs up</InlineCode>,{" "}
            <InlineCode>gs down</InlineCode>) update the mount&apos;s base snapshot without
            rewriting or fetching untouched files; new content is fetched only when read.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Checkpoints, rewind and fork">
        <p>
          A checkpoint snapshots your local edits on this machine so you can return to them later —
          nothing is uploaded. Humans and agents use checkpoints to try an approach and back out
          cheaply.
        </p>
        <CommandBlock label="checkpoint, rewind, and fork commands">{`gs checkpoint -m "tests pass, before the refactor"
gs checkpoint list           # newest first, numbered
gs rewind 3                  # restore local edits from checkpoint 3
gs rewind                    # restore the newest checkpoint that differs from now
gs fork 3 ~/work/payments-b  # mount checkpoint 3 as a second workspace`}</CommandBlock>
        <ul className="space-y-2">
          <BulletPoint label="Automatic safety net:">
            <InlineCode>gs</InlineCode> automatically takes a checkpoint before{" "}
            <InlineCode>gs sync</InlineCode>, <InlineCode>gs switch</InlineCode>,{" "}
            <InlineCode>gs restore</InlineCode>, and <InlineCode>gs rewind</InlineCode> modify your
            files, keeping the last 50 automatic checkpoints. Manual checkpoints taken with{" "}
            <InlineCode>gs checkpoint</InlineCode> are kept until the workspace is deleted.
          </BulletPoint>
          <BulletPoint label="Undoable rewinds:">
            Rewinding automatically checkpoints the state it replaces, so running{" "}
            <InlineCode>gs rewind</InlineCode> again undoes the rewind.
          </BulletPoint>
          <BulletPoint label="Side-by-side forks:">
            <InlineCode>gs fork</InlineCode> mounts a new lazy workspace showing the checkpoint on
            the exact base commit it was taken on, so two approaches can be compared side by side.
            (Checkpoints taken before a <InlineCode>gs sync</InlineCode> moved the base are opened
            with <InlineCode>gs fork</InlineCode> rather than rewound in place.)
          </BulletPoint>
        </ul>
        <p>Checkpoints are currently supported in lazy workspaces only.</p>
      </DocSectionCard>

      <DocSectionCard title="Requirements">
        <ul className="space-y-2">
          <BulletPoint label="Linux:">
            FUSE 3 (<InlineCode>/dev/fuse</InlineCode> and <InlineCode>fusermount3</InlineCode>,
            typically from the <InlineCode>fuse3</InlineCode> package). No root privileges required.
          </BulletPoint>
          <BulletPoint label="macOS:">
            <a
              className={linkClass}
              href="https://macfuse.github.io"
              rel="noopener noreferrer"
              target="_blank"
            >
              macFUSE
            </a>
            . After installing, allow its system extension in System Settings → Privacy &amp;
            Security and restart; until approved, mounting waits and exits with a setup hint.
          </BulletPoint>
          <BulletPoint label="Windows:">
            Not supported yet; use a standard checkout with <InlineCode>gs init</InlineCode>.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Troubleshooting">
        <ul className="space-y-2">
          <BulletPoint label="gs doctor:">
            Checks CLI version, authentication, server reachability, and whether a test FUSE mount
            succeeds on this machine.
          </BulletPoint>
          <BulletPoint label="gs mount status [dir]:">
            Shows the workspace&apos;s base commit, how much content has been fetched, and recent
            errors with paths and reasons.
          </BulletPoint>
          <BulletPoint label="Working offline:">
            Cached reads and all local edits keep working without a network connection. Uncached
            reads retry briefly and then fail fast for a cooldown window so builds do not hang on
            every file. Run <InlineCode>gs fetch</InlineCode> before going offline.
          </BulletPoint>
          <BulletPoint label="Stale or dead mount:">
            If the serving process was killed abruptly, run{" "}
            <InlineCode>fusermount3 -u &lt;dir&gt;</InlineCode> (Linux) or{" "}
            <InlineCode>umount &lt;dir&gt;</InlineCode> (macOS) to clear the mount point, then{" "}
            <InlineCode>gs mount &lt;dir&gt;</InlineCode> to reattach the workspace with all local
            edits intact.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Understand accounts, slices, workspaces, changesets, and submit validation."
          section="concepts"
          title="Understand the model"
        />
        <NextDocCard
          description="Full reference for gs mount, gs fetch, gs checkpoint, gs rewind, and gs fork."
          section="cli"
          title="CLI Reference"
        />
      </section>
    </div>
  );
}
