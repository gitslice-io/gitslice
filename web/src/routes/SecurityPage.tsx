import { Link } from "@tanstack/react-router";

import {
  GITHUB_MIRROR_URL,
  RELEASES_URL,
  SECURITY_EMAIL,
  SitePageHeader,
  SiteSection
} from "./site/SitePageChrome";

const securityControls: Array<{ title: string; body: string }> = [
  {
    title: "Encryption in transit and at rest",
    body: "All web, API, Git, and CLI traffic is encrypted over TLS. Folder secrets are stored encrypted at rest and decrypted only when needed for an authorized check or automation run."
  },
  {
    title: "Sealed secrets & host-bound egress",
    body: "By default, automation workers and HTTP-gated containers receive opaque secret placeholders rather than plaintext tokens. Values are unwrapped only on outbound HTTPS requests to hosts you explicitly allowlist in network:, and all logs and step outputs are scrubbed before storage."
  },
  {
    title: "Server-enforced admission & approvals",
    body: "Every landing is admitted by the server against path-scoped owners, required CI checks, and human-only approval rules. Changes under .gitslice/ (checks, owners, and automations) always require account owner or admin approval."
  },
  {
    title: "Contributor & agent isolation",
    body: "Draft patchsets from untrusted contributors run checks without secrets until explicitly allowed by a maintainer. Coding agents authenticate with dedicated agent identities, can be restricted from human-only approval gates and manual runbooks, and leave an immutable patchset audit trail."
  },
  {
    title: "Caller-bounded template ceilings",
    body: "Reusable on:[call] automation templates never read secrets from the template's folder, cannot escalate to plaintext reveal or direct internet egress without explicit caller opt-in, and require immutable tag or commit SHA pins across accounts."
  },
  {
    title: "Zero lock-in & full open-source auditability",
    body: "Gitslice is licensed under Apache 2.0 and developed in the open. You can inspect the server, CLI, and web app source, self-host your own instance, clone any slice over standard Git, or enable automatic fast-forward Git mirrors."
  }
];

export function SecurityPage() {
  return (
    <article className="mx-auto w-full max-w-4xl pb-16">
      <SitePageHeader
        description="How Gitslice protects your source code, encrypted folder secrets, CI runners, and agent workflows — and how to report a vulnerability."
        eyebrow="Trust & Security"
        title="Security at Gitslice"
        updated="2026-11-24"
      />

      <section className="border-b border-slate-200/80 py-8 dark:border-zinc-800/80">
        <h2 className="text-xl font-semibold tracking-tight text-zinc-950 dark:text-zinc-50">
          Security architecture
        </h2>
        <div className="mt-5 grid grid-cols-1 gap-4 sm:grid-cols-2">
          {securityControls.map((item) => (
            <div
              className="rounded-xl border border-slate-200 bg-white p-5 dark:border-zinc-800 dark:bg-zinc-900/50"
              key={item.title}
            >
              <h3 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
                {item.title}
              </h3>
              <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
                {item.body}
              </p>
            </div>
          ))}
        </div>
      </section>

      <SiteSection id="verified-installs" title="Verified CLI releases">
        <p>
          The installer at{" "}
          <a
            className="font-mono text-xs font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href="/install.sh"
          >
            https://gitslice.io/install.sh
          </a>{" "}
          downloads prebuilt <code className="font-mono text-xs">gs</code> binaries from{" "}
          <a
            className="font-mono text-xs font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href={RELEASES_URL}
          >
            gitslice.io/releases
          </a>{" "}
          and verifies SHA-256 checksums before installation. You can also build directly from
          source with Go:
        </p>
        <pre
          aria-label="Build gs CLI from source"
          className="overflow-x-auto rounded-lg border border-slate-200 bg-slate-950 p-3.5 font-mono text-xs text-slate-100 dark:border-zinc-800"
          role="region"
          tabIndex={0}
        >
          <code>go install gitslice.io/gitslice/cmd/gs@latest</code>
        </pre>
        <p>
          Full source code is auditable on{" "}
          <Link
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            params={{ account: "gitslice", slice: "gitslice" }}
            to="/slices/$account/$slice"
          >
            gitslice/gitslice
          </Link>{" "}
          and mirrored to{" "}
          <a
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href={GITHUB_MIRROR_URL}
            rel="noopener noreferrer"
            target="_blank"
          >
            GitHub
          </a>
          .
        </p>
      </SiteSection>

      <SiteSection id="disclosure" title="Responsible vulnerability disclosure">
        <p>
          If you discover a security vulnerability in Gitslice, please report it privately to{" "}
          <a
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href={`mailto:${SECURITY_EMAIL}`}
          >
            {SECURITY_EMAIL}
          </a>{" "}
          before opening a public issue. Our RFC 9116 metadata is published at{" "}
          <a
            className="font-mono text-xs font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href="/.well-known/security.txt"
          >
            /.well-known/security.txt
          </a>
          .
        </p>
        <ul className="list-disc space-y-1.5 pl-5">
          <li>Include reproduction steps, affected endpoints or CLI commands, and potential impact.</li>
          <li>We acknowledge reports within 2 business days and coordinate remediation and disclosure timelines with you.</li>
          <li>Please avoid testing against other users&apos; private slices or running denial-of-service traffic against production.</li>
        </ul>
      </SiteSection>
    </article>
  );
}
