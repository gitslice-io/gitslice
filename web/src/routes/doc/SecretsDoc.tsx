import { PageHeader, CommandBlock, NextDocCard, inlineCode } from "./DocParts";

const sectionClass = "mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5";
const headingClass = "text-base font-semibold text-zinc-950 dark:text-zinc-50";
const textClass = "mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400";
const strongClass = "font-semibold text-zinc-950 dark:text-zinc-50";

const accessRows: Array<[string, string]> = [
  [
    "Account owners and admins",
    "Create, change, rescope and delete secrets. Read values back into a local env file with gs secret env."
  ],
  [
    "Slice writers and slice admins",
    "See the names that reach the slice (gs slice secret ls). Their changesets' checks receive the values; they cannot read them out."
  ],
  ["Readers and everyone else", "Nothing: not the names, not the values."]
];

export function SecretsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Secrets"
        description="A secret is a token, key or password that Gitslice keeps encrypted and gives to your CI checks as an environment variable, so it never has to live in the repository."
      />

      <section className={sectionClass}>
        <h2 className={headingClass}>How secrets work</h2>
        <ul className="mt-4 space-y-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          <li>
            <span className={strongClass}>An account owns it.</span> A secret belongs to an account (yours, or an
            organization&apos;s) and applies to some of that account&apos;s slices, or to all of them, including slices
            created later.
          </li>
          <li>
            <span className={strongClass}>Checks read it as an environment variable</span> named after the secret, such
            as <code className={inlineCode}>DEPLOY_TOKEN</code>. The checks that the slice&apos;s CI daemon runs get
            every secret that reaches the slice. A secret replaces a check&apos;s own <code className={inlineCode}>env</code>{" "}
            entry of the same name. A slice without a CI daemon passes its secrets to nothing.
          </li>
          <li>
            <span className={strongClass}>One value per name per slice.</span> One name can hold different values for
            different slices, for example a staging token for <code className={inlineCode}>web</code> and a production
            token for <code className={inlineCode}>api</code>. A secret that lists a slice overrides an all-slices secret
            of the same name there.
          </li>
          <li>
            <span className={strongClass}>Values stay hidden.</span> They are stored encrypted, no page shows them, and a
            check&apos;s log and summary show them as <code className={inlineCode}>***</code>. Values shorter than 4
            characters are not masked, and a value of several lines is also masked line by line.
          </li>
        </ul>
      </section>

      <section className={sectionClass}>
        <h2 className={headingClass}>Add a secret</h2>
        <p className={textClass}>
          In the web app, open your account&apos;s page and choose <span className={strongClass}>Secrets</span>, or use
          the Secrets panel in a slice&apos;s <span className={strongClass}>Settings</span>. From the CLI, pipe the value
          in so it stays out of your shell history (in a terminal, leave out the pipe and type it; it is not echoed):
        </p>
        <CommandBlock>{`# for some slices of your active account (--account to pick another)
printf %s "$TOKEN" | gs secret create DEPLOY_TOKEN --slice api --slice worker

# for every slice of the account, including later ones
printf %s "$NPM_TOKEN" | gs secret create NPM_TOKEN --all-slices

gs secret ls                                       # names, scopes, who changed them last
gs secret update <secret-id> --add-slice web       # or --remove-slice, --slice ..., --all-slices
printf %s "$NEW" | gs secret update <secret-id> --value
gs secret rm <secret-id>`}</CommandBlock>
        <p className={textClass}>
          Names are environment variable names: capital letters, digits and <code className={inlineCode}>_</code>, not
          starting with a digit. A value can be up to 48 KiB, so a key file fits, and an account can have up to 200
          secrets.
        </p>
      </section>

      <section className={sectionClass}>
        <h2 className={headingClass}>One slice at a time</h2>
        <p className={textClass}>
          <code className={inlineCode}>gs slice secret</code> works on a single slice. It changes only secrets that
          belong to that slice alone, so it never changes a value that other slices share:
        </p>
        <CommandBlock>{`gs slice secret ls acme/api                      # the names that reach the slice
printf %s "$TOKEN" | gs slice secret set acme/api CI_TOKEN
gs slice secret rm acme/api CI_TOKEN             # takes the slice out of a shared secret`}</CommandBlock>
        <p className={textClass}>
          <code className={inlineCode}>set</code> refuses a name that a secret shared with other slices already gives
          this slice; change that one with <code className={inlineCode}>gs secret update</code>. <code className={inlineCode}>rm</code>{" "}
          refuses an all-slices secret, because removing it would affect every slice.
        </p>
      </section>

      <section className={sectionClass}>
        <h2 className={headingClass}>Who can see what</h2>
        <div className="mt-4 overflow-x-auto">
          <table className="min-w-full divide-y divide-slate-200 dark:divide-zinc-800 text-left text-sm">
            <thead className="bg-slate-50 dark:bg-zinc-950 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
              <tr>
                <th className="px-4 py-3">Who</th>
                <th className="px-4 py-3">What they can do</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-zinc-800">
              {accessRows.map(([who, what]) => (
                <tr key={who}>
                  <td className="whitespace-nowrap px-4 py-3 font-medium text-zinc-950 dark:text-zinc-50">{who}</td>
                  <td className="px-4 py-3 text-slate-600 dark:text-zinc-400">{what}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className={textClass}>
          <span className={strongClass}>Scope with care.</span> A check runs whatever its changeset says, so anyone who
          can write to a slice can read its secrets from a check, and the slice&apos;s CI daemon receives them too.
          Masking with <code className={inlineCode}>***</code> catches accidental prints, such as a token echoed in a
          debug line, but it does not stop a check that sets out to leak a value, for example by encoding it. Give a
          secret only to the slices whose writers you would trust with it.
        </p>
      </section>

      <section className={sectionClass}>
        <h2 className={headingClass}>Fill a local env file</h2>
        <p className={textClass}>
          Some work needs the same values on your own machine, a deploy for example. Commit a template that names every
          variable, with public values written out and each secret as a placeholder:
        </p>
        <CommandBlock>{`# env.prod.template
PUBLIC_API_BASE_URL=https://api.example.com
CLOUDFLARE_API_TOKEN=gitslice-secret:CLOUDFLARE_API_TOKEN
R2_SECRET_ACCESS_KEY=gitslice-secret:R2_SECRET_ACCESS_KEY`}</CommandBlock>
        <p className={textClass}>Then the account&apos;s owners and admins fill it in:</p>
        <CommandBlock>{`gs secret env env.prod.template --slice acme/api -o .env.prod`}</CommandBlock>
        <ul className="mt-4 space-y-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          <li>
            The file is written readable by you only (mode 600). <code className={inlineCode}>gs</code> refuses to
            print values to a terminal and to replace an existing file without{" "}
            <code className={inlineCode}>--force</code>.
          </li>
          <li>
            If a placeholder names a secret the slice doesn&apos;t have, nothing is written. Values a shell would
            misread are quoted, so the file can be sourced.
          </li>
          <li>
            Only owners and admins of the slice&apos;s account can read values this way, and each read is logged with
            who asked and which names. Everyone else uses an example env file without production credentials.
          </li>
          <li>
            After rotating a key, update the secret and generate the file again. Keep the file out of the repository
            (add it to <code className={inlineCode}>.gitignore</code>).
          </li>
        </ul>
        <p className={textClass}>
          Gitslice deploys its own website this way: <code className={inlineCode}>env.prod.template</code> in the{" "}
          <code className={inlineCode}>gitslice/gitslice</code> slice.
        </p>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Define the checks that receive these secrets, and run a CI daemon for them."
          section="checks"
          title="Set up CI checks"
        />
        <NextDocCard description="Every gs secret command at a glance." section="cli" title="CLI reference" />
      </section>
    </div>
  );
}
