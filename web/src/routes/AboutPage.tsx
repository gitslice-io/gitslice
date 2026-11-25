import { Link } from "@tanstack/react-router";

import {
  CONTACT_EMAIL,
  GITHUB_ISSUES_URL,
  GITHUB_MIRROR_URL,
  SECURITY_EMAIL,
  SitePageHeader,
  SiteSection
} from "./site/SitePageChrome";

export function AboutPage() {
  return (
    <article className="mx-auto w-full max-w-4xl pb-16">
      <SitePageHeader
        description="Gitslice builds Git-compatible source infrastructure designed for engineering teams and fleets of coding agents working in parallel on one codebase."
        eyebrow="About Gitslice"
        title="Source infrastructure built for parallel work"
      />

      <SiteSection id="mission" title="Why we built Gitslice">
        <p>
          Traditional Git workflows assume one human pushing one branch at a time. When dozens of
          engineers and autonomous coding agents work across a shared codebase, single-branch
          pushes turn into retry storms, full-repo clones waste bandwidth, and security rules
          scatter across disconnected CI scripts.
        </p>
        <p>
          Gitslice replaces branch serialization with a native content-addressed source graph,
          focused slice views, server-enforced admission gates, and folder-scoped automations —
          while staying fully Git-compatible at the edges so your existing tooling and backup
          guarantees never break.
        </p>
      </SiteSection>

      <SiteSection id="dogfooded" title="Built, tested, and operated on Gitslice">
        <p>
          Gitslice is developed inside Gitslice itself. Every changeset lands through our own
          path-level admission checks, required CI runners, human approval gates on{" "}
          <code className="font-mono text-xs">.gitslice/</code> policy files, folder-scoped
          deployment automations, and real-time mirroring to GitHub.
        </p>
        <ul className="list-disc space-y-1.5 pl-5">
          <li>
            <strong>Open source (Apache 2.0):</strong> Browse the live source tree at{" "}
            <Link
              className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
              params={{ account: "gitslice", slice: "gitslice" }}
              to="/slices/$account/$slice"
            >
              gitslice/gitslice
            </Link>{" "}
            or on our{" "}
            <a
              className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
              href={GITHUB_MIRROR_URL}
              rel="noopener noreferrer"
              target="_blank"
            >
              GitHub mirror
            </a>
            .
          </li>
          <li>
            <strong>Transparent benchmarks:</strong> Every performance claim on our homepage is
            tied to reproducible benchmark suites published alongside our source code.
          </li>
        </ul>
      </SiteSection>

      <SiteSection id="contact" title="Contact & community">
        <ul className="list-disc space-y-1.5 pl-5">
          <li>
            <strong>General inquiries &amp; support:</strong>{" "}
            <a
              className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
              href={`mailto:${CONTACT_EMAIL}`}
            >
              {CONTACT_EMAIL}
            </a>
          </li>
          <li>
            <strong>Security reports:</strong>{" "}
            <a
              className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
              href={`mailto:${SECURITY_EMAIL}`}
            >
              {SECURITY_EMAIL}
            </a>{" "}
            (see{" "}
            <Link
              className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
              to="/security"
            >
              Security
            </Link>
            )
          </li>
          <li>
            <strong>Bug reports &amp; feature requests:</strong>{" "}
            <a
              className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
              href={GITHUB_ISSUES_URL}
              rel="noopener noreferrer"
              target="_blank"
            >
              github.com/gitslice-io/gitslice/issues
            </a>
          </li>
        </ul>
      </SiteSection>
    </article>
  );
}
