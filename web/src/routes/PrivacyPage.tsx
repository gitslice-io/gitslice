import { Link } from "@tanstack/react-router";

import {
  CONTACT_EMAIL,
  SitePageHeader,
  SiteSection
} from "./site/SitePageChrome";

export function PrivacyPage() {
  return (
    <article className="mx-auto w-full max-w-4xl pb-16">
      <SitePageHeader
        description="How Gitslice collects, uses, stores, and protects account and repository information."
        eyebrow="Legal"
        title="Privacy Policy"
        updated="2026-11-24"
      />

      <SiteSection id="data-collected" title="1. Information we collect">
        <ul className="list-disc space-y-1.5 pl-5">
          <li>
            <strong>Account &amp; authentication data:</strong> Username, email address, display
            name, avatar, organization memberships, and delegated agent identities associated with
            your account.
          </li>
          <li>
            <strong>Source code &amp; workflow artifacts:</strong> Files, commits, changesets,
            reviews, check logs, automation run logs, and encrypted secrets you store on Gitslice.
          </li>
          <li>
            <strong>Operational &amp; security logs:</strong> Request metadata, IP addresses, API
            call status, and audit records (such as secret access and approval events) required to
            operate and secure the service.
          </li>
        </ul>
      </SiteSection>

      <SiteSection id="how-used" title="2. How we use information">
        <p>
          We use your data solely to provide, maintain, and secure Gitslice — including hosting
          your slices, running configured checks and automations, mirroring to Git remotes you
          configure, preventing abuse, and responding to support requests.
        </p>
        <p>
          <strong>We do not sell your personal information or train AI models on your private source code.</strong>
        </p>
      </SiteSection>

      <SiteSection id="secrets-handling" title="3. Secrets & private slices">
        <p>
          Private slices are accessible only to authorized account members, slice collaborators,
          and delegated agents you grant access to. Secrets are encrypted at rest, never displayed
          in the web UI, masked in logs and run outputs, and exposed only to authorized runs. See
          our{" "}
          <Link
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            to="/security"
          >
            Security page
          </Link>{" "}
          for technical controls.
        </p>
      </SiteSection>

      <SiteSection id="portability" title="4. Data portability, retention & deletion">
        <p>
          You can export any slice at any time using standard <code className="font-mono text-xs">git clone</code>{" "}
          or automatic Slice Git Mirrors. To request deletion of your account and associated
          private data, email{" "}
          <a
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href={`mailto:${CONTACT_EMAIL}`}
          >
            {CONTACT_EMAIL}
          </a>
          . Public commits and open-source contributions shared with other accounts may remain
          part of shared commit history.
        </p>
      </SiteSection>

      <SiteSection id="contact" title="5. Contact">
        <p>
          Questions about this Privacy Policy may be sent to{" "}
          <a
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href={`mailto:${CONTACT_EMAIL}`}
          >
            {CONTACT_EMAIL}
          </a>
          .
        </p>
      </SiteSection>
    </article>
  );
}
