import { PageHeader, CommandBlock, inlineCode } from "./DocParts";
import { webhookEvents, webhookHeaders } from "./docData";

export function WebhooksDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Webhooks"
        description="A webhook POSTs a JSON event to your HTTPS endpoint when something happens in a slice, so a build, a deploy or a chat bot can react without polling."
      />

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Add a webhook</h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Webhooks belong to a slice, and only the slice&apos;s owners and admins manage them. In the
          web app, open the slice&apos;s <span className="font-semibold text-zinc-950 dark:text-zinc-50">Settings</span>{" "}
          and use the Webhooks panel. From the CLI:
        </p>
        <CommandBlock>{`# the secret is read from stdin so it stays out of your shell history
printf %s "$SECRET" | gs webhook create --slice acme/payment \\
  --url https://example.com/gitslice --event tag.created --event push --secret-stdin

gs webhook ping <webhook-id>          # send a ping now
gs webhook deliveries <webhook-id>    # what was sent and how it went
gs webhook redeliver <delivery-id>    # send one again`}</CommandBlock>
        <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          URLs must be <code className={inlineCode}>https://</code> and reach a public address. A slice can
          have up to 20 webhooks. The secret is stored encrypted and never shown again. Query values in a
          URL (often a token) are shown as <code className={inlineCode}>...</code> in lists.
        </p>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Events</h2>
        <div className="mt-4 overflow-x-auto">
          <table className="min-w-full divide-y divide-slate-200 dark:divide-zinc-800 text-left text-sm">
            <thead className="bg-slate-50 dark:bg-zinc-950 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
              <tr>
                <th className="px-4 py-3">Event</th>
                <th className="px-4 py-3">When</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-zinc-800">
              {webhookEvents.map(([name, when]) => (
                <tr key={name}>
                  <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-zinc-950 dark:text-zinc-50">{name}</td>
                  <td className="px-4 py-3 text-slate-600 dark:text-zinc-400">{when}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Payload</h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Every body has the same envelope, plus a section for the event:{" "}
          <code className={inlineCode}>tag</code>, <code className={inlineCode}>changeset</code>,{" "}
          <code className={inlineCode}>commit</code> or <code className={inlineCode}>check_run</code>.
        </p>
        <CommandBlock>{`{
  "id": "evt_…",
  "event": "tag.created",
  "created_at": "2026-10-08T16:00:00Z",
  "slice": {
    "account": "acme", "name": "payment", "full_name": "acme/payment",
    "url": "https://gitslice.io/slices/acme/payment"
  },
  "sender": { "username": "ann" },
  "tag": { "name": "v1.2.0", "commit_id": "sha256:…", "message": "Release v1.2.0" }
}`}</CommandBlock>
        <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          A <code className={inlineCode}>changeset</code> section has its id, handle, title, status, author,
          target ref, patchset and a link. A <code className={inlineCode}>push</code> to another slice
          describes only the commit and the changed paths inside that slice.
        </p>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Headers and signatures</h2>
        <dl className="mt-4 space-y-3">
          {webhookHeaders.map(([name, meaning]) => (
            <div key={name}>
              <dt>
                <code className="rounded-md bg-slate-50 dark:bg-zinc-950 px-2 py-1 font-mono text-xs text-slate-700 dark:text-zinc-300">
                  {name}
                </code>
              </dt>
              <dd className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">{meaning}</dd>
            </div>
          ))}
        </dl>
        <p className="mt-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          To check a delivery, compute the HMAC of the raw request body with your secret and compare it to
          the header in constant time:
        </p>
        <CommandBlock>{`// Node.js
import { createHmac, timingSafeEqual } from "node:crypto";

function verify(secret, rawBody, header) {
  const want = "sha256=" + createHmac("sha256", secret).update(rawBody).digest("hex");
  return header?.length === want.length && timingSafeEqual(Buffer.from(header), Buffer.from(want));
}`}</CommandBlock>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Delivery and retries</h2>
        <ul className="mt-4 space-y-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Success:</span> any 2xx answer
            within 10 seconds. Redirects are not followed.
          </li>
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Retries:</span> a failed delivery
            is tried again after 1 minute, 5 minutes, 30 minutes, 2 hours, 6 hours and 12 hours, then marked
            failed. Redeliver it any time from the delivery log.
          </li>
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">At least once, any order:</span>{" "}
            the same event can arrive twice and events can arrive out of order, so use{" "}
            <code className={inlineCode}>X-Gitslice-Event-ID</code> to drop duplicates.
          </li>
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">History:</span> deliveries,
            with their request and response bodies, are kept for 30 days.
          </li>
        </ul>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Example: release on tag</h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Gitslice builds its own <code className={inlineCode}>gs</code> releases this way: the{" "}
          <code className={inlineCode}>gitslice/gitslice</code> slice sends{" "}
          <code className={inlineCode}>tag.created</code> to a Cloud Build webhook trigger, which reads the
          tag name from <code className={inlineCode}>$(body.tag.name)</code> and builds it. Creating a tag
          is all it takes:
        </p>
        <CommandBlock>{`gs tag create v0.5.0 --slice gitslice/gitslice -m "Release v0.5.0"`}</CommandBlock>
      </section>
    </div>
  );
}
