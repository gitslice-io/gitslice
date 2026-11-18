export const markdown = `Your code lives on Gitslice, but you often still want an up-to-date copy on GitHub — as an independent backup, for people browsing where they already look, or to run existing GitHub Actions workflows. You can configure that directly on any slice with two commands:

\`\`\`bash
gs slice secret set acme/app GITHUB_MIRROR_TOKEN < token.txt
gs slice mirror set acme/app --url https://github.com/acme/app.git --token-secret GITHUB_MIRROR_TOKEN
\`\`\`

Once set, every landed changeset and new tag on the slice is pushed to your GitHub repository within seconds. There is no CI workflow to maintain, no cron job to schedule, and nothing to keep running on your machine.

## 1. One slice, one mirror

In Git terms, a slice behaves like a repository with a main branch and tags, so a mirror maps one slice to one remote repository.

Both the target URL and secret reference live in the slice's settings rather than inside versioned config files. A changeset can never redirect your mirror elsewhere, and only account owners and admins can view or update mirror settings.

## 2. What gets pushed

- **The main branch.** Every landed commit is mirrored with its author, timestamps, and commit message, plus a \`Gitslice-Commit\` trailer linking back to the Gitslice commit.
- **Natural repository roots.** If your slice covers one top-level folder such as \`/acme/app\`, that folder becomes the root of the mirrored repository — matching what contributors expect on GitHub rather than nesting everything under \`acme/app/\`. Slices spanning multiple folders preserve their full paths.
- **Tags.** Newly created tags are pushed once; existing remote tags are never overwritten or moved.

## 3. Never force-pushes

A backup that can overwrite history isn't much of a backup. Gitslice mirrors only advance your remote branch via fast-forward pushes:

- **Empty repository:** receives the slice's full commit history on the initial push.
- **Pre-existing mirror:** resumes seamlessly from the shared commit, making it painless to switch away from a custom sync script.
- **Diverged remote history:** if someone commits directly to the GitHub repository outside Gitslice, the mirror pauses instead of force-pushing. Nothing is overwritten; Gitslice records an error explaining that the remote contains commits not created in Gitslice so you can decide how to reconcile them.

## 4. Retries and status visibility

If a remote host is unreachable or rejects a push, Gitslice keeps the update pending and retries automatically after 1 minute, 5 minutes, 30 minutes, 2 hours, and 6 hours, then every 12 hours thereafter. No changes are lost while waiting — the next successful push brings the repository completely up to date.

You can check the last pushed commit, pending status, and latest error anytime with \`gs slice mirror acme/app\` or in the slice settings UI. If a token expires, store a new one under the same secret name and trigger an immediate push:

\`\`\`bash
gs slice secret set acme/app GITHUB_MIRROR_TOKEN < new-token.txt
gs slice mirror push acme/app
\`\`\`

## 5. Tokens and transport safety

Your push credential is stored as an account [secret](/doc/secrets). The mirror stores only the secret's name, and token values are never exposed in mirror status output or error messages.

For GitHub, create a fine-grained personal access token scoped strictly to **Contents (Read and write)** on that single repository. GitLab, Forgejo, and other Git hosts work too — pass \`--username\` if your host expects an explicit username alongside its token.

To protect credentials and block private-network redirects, mirrors push only over \`https://\` to public hosts and refuse HTTP redirects.

## 6. Try it

\`\`\`bash
gs slice mirror set acme/app --url https://github.com/acme/app.git --token-secret GITHUB_MIRROR_TOKEN
gs slice mirror acme/app        # view last pushed commit, pending state, and errors
gs slice mirror push acme/app   # push immediately
gs slice mirror rm acme/app     # stop mirroring
\`\`\`

You can also configure everything in the web UI under a slice's **Settings -> Mirror to Git** panel (right below Secrets so you can add the token first). CLI commands require \`gs\` v0.4.25 or later (\`gs upgrade\`). See the [Git Mirrors docs](/doc/mirrors) for step-by-step GitHub token creation and divergence recovery.

## 7. Built and mirrored on Gitslice

Gitslice is developed inside Gitslice itself, and [gitslice-io/gitslice](https://github.com/gitslice-io/gitslice) on GitHub is kept current by this feature. Previously, an hourly scheduled job copied commits over and routinely lagged behind. When we turned on mirroring, the initial sync recognized every commit already on GitHub, sent nothing extra, and landed the next changeset on GitHub 9 seconds after submit — triggering our GitHub Actions workflows automatically.`;
