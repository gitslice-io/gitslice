import {
  BulletPoint,
  CommandBlock,
  DocLink,
  DocSectionCard,
  DocTable,
  InlineCode,
  NextDocCard,
  PageHeader
} from "./DocParts";

const accessRows: Array<[string, string]> = [
  [
    "Account owners and admins",
    "Create, update, move, and delete secrets. Render values into a local mode-600 env file with gs secret env."
  ],
  [
    "Slice writers and slice admins",
    "List secret names that apply in the slice (gs slice secret ls). Their changesets' checks receive secret values, and they can allow contributor patchsets via gs check allow, but they cannot read raw values out."
  ],
  [
    "Slice contributors",
    "Cannot list secret names or values. Their changesets' checks run without secrets until a writer or admin runs gs check allow."
  ],
  ["Readers and everyone else", "No access to secret names or values."]
];

export function SecretsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Secrets"
        description="Store tokens, API keys, and passwords encrypted in Gitslice and scope them to folders — for CI checks, triggers, Git mirrors, and local env files — without ever committing credentials."
      />

      <DocSectionCard title="How secrets work">
        <ul className="space-y-2.5">
          <BulletPoint label="Folder-scoped under an account:">
            A secret belongs to a personal or organization account and applies to the folders you
            attach it to plus everything below them (for example,{" "}
            <InlineCode>/acme/services/billing</InlineCode>). Attaching it to{" "}
            <InlineCode>/acme</InlineCode> covers the entire account.
          </BulletPoint>
          <BulletPoint label="Injected into checks, triggers, and mirrors:">
            <DocLink section="checks">CI checks</DocLink> read secrets as environment variables
            matching the secret name (such as <InlineCode>DEPLOY_TOKEN</InlineCode>), based on the
            folder of the <InlineCode>.gitslice/checks.yaml</InlineCode> that defines the check. A
            secret overrides a check&apos;s own <InlineCode>env</InlineCode> entry of the same name.{" "}
            <DocLink section="triggers">Triggers</DocLink> and{" "}
            <DocLink section="mirrors">Git mirrors</DocLink> reference the same folder and slice
            secrets by name. Checks only receive secrets when running on the slice&apos;s CI daemon.
          </BulletPoint>
          <BulletPoint label="Closest folder wins:">
            A folder holds at most one secret per name. When multiple ancestor folders define the
            same name, the closest folder takes precedence: a{" "}
            <InlineCode>DEPLOY_TOKEN</InlineCode> on <InlineCode>/acme/web</InlineCode> overrides
            one on <InlineCode>/acme</InlineCode> for anything under{" "}
            <InlineCode>/acme/web</InlineCode>.
          </BulletPoint>
          <BulletPoint label="Encrypted and masked in logs:">
            Values are stored encrypted, never shown in the web UI, and masked as{" "}
            <InlineCode>***</InlineCode> in check and trigger logs (multi-line values are masked
            line by line; values shorter than 4 characters are not masked).
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Add and manage secrets">
        <p>
          In the web app, open{" "}
          <span className="font-semibold text-zinc-950 dark:text-zinc-50">Policy → Secrets</span> on
          any folder while browsing to see inherited secrets or add one to that folder, or open{" "}
          <span className="font-semibold text-zinc-950 dark:text-zinc-50">Secrets</span> on your
          account page to manage every folder secret in one place. From the CLI, pipe the value via
          stdin (omitting <InlineCode>--path</InlineCode> inside a workspace defaults to the current
          folder):
        </p>
        <CommandBlock label="secret management commands">{`# inside a workspace folder, defaults to the current folder
printf %s "$TOKEN" | gs secret create WEB_TOKEN

# scope to specific folders in your active account (pass --account for another)
printf %s "$TOKEN" | gs secret create DEPLOY_TOKEN --path /acme/api --path /acme/worker

# scope to the whole account monorepo
printf %s "$NPM_TOKEN" | gs secret create NPM_TOKEN --path /acme

gs secret ls                                       # names, folders, last updater
gs secret update <secret-id> --add-path /acme/web  # or --remove-path, --path
printf %s "$NEW" | gs secret update <secret-id> --value
gs secret rm <secret-id>`}</CommandBlock>
        <p>
          Secret names follow environment variable rules: uppercase letters, digits, and{" "}
          <InlineCode>_</InlineCode>, not starting with a digit. Values can be up to 48 KiB (large
          enough for certificates or key files). Each secret can attach to up to 50 folders, and an
          account can store up to 200 secrets.
        </p>
      </DocSectionCard>

      <DocSectionCard title="One slice at a time">
        <p>
          <InlineCode>gs slice secret</InlineCode> operates strictly on a slice&apos;s included
          folders so you never accidentally alter a secret shared with other folders:
        </p>
        <CommandBlock label="slice-scoped secret commands">{`gs slice secret ls acme/api                      # names that apply in the slice
printf %s "$TOKEN" | gs slice secret set acme/api CI_TOKEN
gs slice secret rm acme/api CI_TOKEN             # detach the slice's folders from the secret`}</CommandBlock>
        <p>
          <InlineCode>set</InlineCode> refuses to overwrite a secret that also applies outside the
          slice (use <InlineCode>gs secret update</InlineCode> instead). <InlineCode>rm</InlineCode>{" "}
          refuses to remove a secret inherited from a folder above the slice, such as the account
          root, because doing so would affect sibling folders.
        </p>
      </DocSectionCard>

      <DocSectionCard title="Who can see what">
        <DocTable
          ariaLabel="Secret access by role"
          head={["Who", "What they can do"]}
          rows={accessRows}
        />
        <p>
          <span className="font-semibold text-zinc-950 dark:text-zinc-50">
            Choose folders and roles with care.
          </span>{" "}
          A check executes commands from its changeset, so any writer of a slice that includes a
          secret&apos;s folder (or a folder above or inside it) can access that secret during CI,
          and that slice&apos;s CI daemon receives it too. Log masking (<InlineCode>***</InlineCode>
          ) catches accidental prints, not deliberate exfiltration such as base64-encoding a value.
          Attach secrets only to folders whose writers you trust, and assign external collaborators
          or autonomous agents the <InlineCode>contributor</InlineCode> role so their patchsets run
          secretless until explicitly allowed with <InlineCode>gs check allow</InlineCode>.
        </p>
      </DocSectionCard>

      <DocSectionCard title="Fill a local env file">
        <p>
          When local tasks such as manual deploys need the same secrets, commit a template file with
          public config written out and secret values marked as placeholders:
        </p>
        <CommandBlock label="env.prod.template example">{`# env.prod.template
PUBLIC_API_BASE_URL=https://api.example.com
CLOUDFLARE_API_TOKEN=gitslice-secret:CLOUDFLARE_API_TOKEN
R2_SECRET_ACCESS_KEY=gitslice-secret:R2_SECRET_ACCESS_KEY`}</CommandBlock>
        <p>
          Account owners and admins fill the template locally. Inside a workspace, secrets are
          resolved from the template&apos;s folder automatically; outside a workspace, pass{" "}
          <InlineCode>--path</InlineCode>:
        </p>
        <CommandBlock label="fill local env file commands">{`gs secret env acme/api/env.prod.template -o .env.prod    # inside a workspace
gs secret env env.prod.template --path /acme/api -o .env.prod`}</CommandBlock>
        <ul className="space-y-2">
          <li>
            The output file is written with permissions <InlineCode>0600</InlineCode> (readable only
            by you). <InlineCode>gs</InlineCode> refuses to print secret values directly to a
            terminal and will not overwrite an existing file without <InlineCode>--force</InlineCode>.
          </li>
          <li>
            If any placeholder references a secret that does not apply at the target folder, nothing
            is written. Values containing shell metacharacters are quoted so the output can be
            sourced safely.
          </li>
          <li>
            Only account owners and admins can run <InlineCode>gs secret env</InlineCode>, and every
            read is audit-logged with the caller and secret names. Everyone else should work from an
            example env file without production credentials.
          </li>
          <li>
            After rotating a credential, update the secret and regenerate the local file. Keep
            generated env files out of source control via <InlineCode>.gitignore</InlineCode>.
          </li>
        </ul>
        <p>
          Gitslice deploys its own web app using this pattern — see{" "}
          <InlineCode>env.prod.template</InlineCode> in the{" "}
          <InlineCode>gitslice/gitslice</InlineCode> slice.
        </p>
      </DocSectionCard>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Define the checks that consume these secrets and run a CI daemon."
          section="checks"
          title="CI Checks"
        />
        <NextDocCard
          description="Use folder secrets to sign webhooks, call external CI, or authenticate scripts."
          section="triggers"
          title="Triggers"
        />
      </section>
    </div>
  );
}
