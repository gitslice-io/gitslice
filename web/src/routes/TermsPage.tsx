import { Link } from "@tanstack/react-router";

import {
  CONTACT_EMAIL,
  GITHUB_MIRROR_URL,
  SitePageHeader,
  SiteSection
} from "./site/SitePageChrome";

export function TermsPage() {
  return (
    <article className="mx-auto w-full max-w-4xl pb-16">
      <SitePageHeader
        description="The terms governing use of Gitslice hosted services at gitslice.io and api.gitslice.io."
        eyebrow="Legal"
        title="Terms of Service"
        updated="2026-11-24"
      />

      <SiteSection id="accounts" title="1. Accounts & delegated agents">
        <p>
          You are responsible for maintaining the confidentiality of your credentials, API keys,
          and any coding agents or CI runner daemons registered under your account. Actions
          performed by delegated agents or automations configured in your account are your
          responsibility.
        </p>
      </SiteSection>

      <SiteSection id="ownership" title="2. Your code belongs to you">
        <p>
          You retain full ownership and intellectual property rights to all source code, data,
          and content you upload to Gitslice. You grant Gitslice only the limited license
          necessary to store, process, display, back up, run your configured automations on, and
          mirror your content as directed by your settings.
        </p>
        <p>
          Gitslice&apos;s own open-source software is licensed separately under the{" "}
          <a
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href={`${GITHUB_MIRROR_URL}/blob/main/LICENSE`}
            rel="noopener noreferrer"
            target="_blank"
          >
            Apache License 2.0
          </a>
          .
        </p>
      </SiteSection>

      <SiteSection id="acceptable-use" title="3. Acceptable use">
        <p>You agree not to use Gitslice to:</p>
        <ul className="list-disc space-y-1.5 pl-5">
          <li>Violate applicable law or infringe the intellectual property rights of others.</li>
          <li>Distribute malware, launch denial-of-service attacks, mine cryptocurrency, or abuse container/automation compute.</li>
          <li>Attempt to access private slices, secrets, or infrastructure belonging to other accounts without authorization.</li>
        </ul>
      </SiteSection>

      <SiteSection id="availability" title="4. Service availability & changes">
        <p>
          Gitslice is currently offered in public beta on an &ldquo;as-is&rdquo; and
          &ldquo;as-available&rdquo; basis. While we operate the service with care and publish
          live health checks on our{" "}
          <Link
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            to="/status"
          >
            Status page
          </Link>
          , we encourage teams to enable automatic Slice Git Mirrors for independent off-platform
          backups. We will provide advance notice before introducing paid billing or material
          changes to these terms.
        </p>
      </SiteSection>

      <SiteSection id="contact" title="5. Contact">
        <p>
          Questions regarding these Terms of Service can be directed to{" "}
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
