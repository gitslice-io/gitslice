export const markdown = `Most teams end up splitting automation across two uncomfortable places: fragile CI glue committed at the top of a repository, and ad-hoc deploy scripts on someone's laptop with production credentials pasted into \`.env\` files. Once coding agents join the team, handing out raw keys becomes a non-starter.

Gitslice **Automations** solve both problems at once. Put \`.gitslice/automations.yaml\` in any folder to run workflows on repository events or as safe manual runbooks — with typed inputs, folder-scoped secrets, isolated execution, reusable templates, and multi-step pipelines — all governed by the folder hierarchy of your monorepo.

\`\`\`bash
gs auto ls                                           # automations applying at your folder
gs auto run deploy-staging -i version=v1.4.2 --yes --follow
\`\`\`

## 1. Committed beside the code, inherited by folder

An automation lives in \`<folder>/.gitslice/automations.yaml\` and applies to that folder and everything beneath it, whichever slice authored a change. Open the **Policy** drawer on any folder in the web app (or run \`gs policy\`) to see every automation, check, owner rule, and secret in effect.

Four core actions cover everyday work:

- **\`http\`** — delivers an HMAC-signed JSON POST to an external endpoint on events such as \`push\`, \`tag.created\`, or \`changeset.submitted\`.
- **\`worker\`** — runs a JavaScript ES module from \`.gitslice/\` in an isolated sandbox with \`fetch\`, \`gs.checks\`, \`gs.files\`, persistent per-run state, and signed callback URLs for long-running external jobs.
- **\`bash\`** — executes shell commands or scripts in a fresh, single-run container with curated toolchains (\`base\`, \`node\`, \`python\`, \`go\`, \`cloud\`) that is destroyed immediately on exit.
- **\`check\`** — dispatches checks from the folder's \`.gitslice/checks.yaml\` on your CI runner pool, so \`automations.yaml\` decides *when* checks run while \`checks.yaml\` defines *how*.

Definitions and scripts always load from the head of \`refs/global/main\` — never from an unsubmitted draft — and edits under \`.gitslice/\` require account owner or admin approval.

## 2. Capability-based runbooks: let people and agents run ops without holding secrets

Add \`manual\` to \`on:\` to turn any automation into an audited runbook callable from the CLI (\`gs auto run\`) or the web UI's **Run** modal:

\`\`\`yaml
# services/billing/.gitslice/automations.yaml
version: 1
automations:
  deploy-staging:
    description: Deploy billing to staging
    on: [manual]
    manual:
      roles: [admin, writer]
      confirm: "Deploy billing to staging?"
    inputs:
      version: { type: string, required: true, pattern: "^[a-zA-Z0-9._-]+$" }
      region:  { type: choice, options: [us-central1, europe-west1], default: us-central1 }
      dry_run: { type: boolean, default: false }
    secrets: [DEPLOY_API_TOKEN]
    network: [api.example.com]
    concurrency: serial
    action:
      bash:
        runtime: cloud
        checkout: folder
        run: |
          echo "Deploying \$INPUT_VERSION to \$INPUT_REGION (dry_run=\$INPUT_DRY_RUN)"
          curl -fsS -H "Authorization: Bearer \$DEPLOY_API_TOKEN" \\
            "https://api.example.com/deployments?region=\$INPUT_REGION&ref=\$INPUT_VERSION"
\`\`\`

Instead of granting a teammate or coding agent access to \`DEPLOY_API_TOKEN\`, an admin stores the [secret](/doc/secrets) on the folder once and exposes \`deploy-staging\` with typed inputs, optional \`require_human: true\` guards, and a confirmation prompt. Every run records who triggered it (human or agent), the exact input values, and the full live log.

For security, secrets are sealed as opaque placeholders by default: outbound HTTPS requests through your \`network:\` allowlist unwrap them only when sending to an allowlisted host, so raw secret values never enter script or container memory unless you explicitly opt a secret into \`reveal:\`. All log output and run outputs are automatically scrubbed of secret values.

## 3. Reusable templates: no separate marketplace or package manager

Because every folder in Gitslice already has a canonical path and immutable slice tags, any folder can publish reusable automation templates simply by adding \`on: [call]\`:

\`\`\`yaml
# /acme/platform/.gitslice/automations.yaml
version: 1
automations:
  deploy-service:
    description: Standard service rollout
    on: [call]
    inputs:
      service: { type: string, required: true }
      env:     { type: choice, options: [staging, prod], default: staging }
      image:   { type: string, required: true }
    secrets: [DEPLOY_TOKEN]
    network: [api.example.com]
    action:
      bash:
        runtime: cloud
        run: |
          curl -fsS -H "Authorization: Bearer \$DEPLOY_TOKEN" \\
            "https://api.example.com/services/\$INPUT_SERVICE/rollout?env=\$INPUT_ENV&image=\$INPUT_IMAGE"
          gs-auto set-output release_url "https://\$INPUT_SERVICE.\$INPUT_ENV.example.com"
\`\`\`

Any other folder calls it with \`action.uses\`, binds static parameters in \`with:\`, and maps its own folder secrets to the template's secret slots:

\`\`\`yaml
# /acme/services/billing/.gitslice/automations.yaml
version: 1
automations:
  deploy-prod:
    on: [manual]
    manual: { roles: [admin], require_human: true, confirm: "Deploy billing to prod?" }
    secrets:
      DEPLOY_TOKEN: BILLING_PROD_DEPLOY_TOKEN
    network: [api.example.com]
    action:
      uses: /acme/platform/deploy-service@v1.0.0
      with:
        service: billing
        env: prod
\`\`\`

Notice three details that make reuse safe and ergonomic:

- **Unbound inputs stay promptable.** Here \`service\` and \`env\` are bound in \`with:\`, so \`image\` automatically appears on the caller's \`gs auto run\` prompt and web UI Run modal.
- **Pinning matches trust boundaries.** Same-account calls can omit a version pin to track the tree live, or pin \`@<tag>\` / \`@sha256:<commit>\`. Cross-account calls to a public slice must always pin an immutable slice tag or commit SHA.
- **Caller-bounded security ceilings.** Secrets always come from the caller's folder — never the template's. A template cannot reveal secrets, enable direct internet egress, exceed the caller's timeout, or reach hosts outside what a cross-account caller explicitly allows in \`network:\`.

Before adopting a template, inspect its inputs, required secret slots, network hosts, resolved commit, and available tags in one command:

\`\`\`bash
gs auto inspect /acme/platform/deploy-service@v1.0.0
\`\`\`

## 4. Multi-step pipelines with typed outputs

When a workflow spans multiple stages, chain up to 20 sequential steps with \`action.steps\`. Each step can call a reusable template (\`uses\`) or run inline \`bash\`, \`worker\`, \`http\`, or \`check\` actions, emit named outputs (\`gs-auto set-output\` in bash or \`gs.setOutput\` in workers), and pass them downstream with \`\${{ steps.<id>.outputs.<key> }}\`:

\`\`\`yaml
# /acme/services/billing/.gitslice/automations.yaml
version: 1
automations:
  release:
    on: [manual]
    secrets: [DEPLOY_TOKEN, NOTIFY_TOKEN]
    network: [api.example.com]
    action:
      steps:
        - id: build
          bash:
            runtime: go
            checkout: folder
            run: |
              TAG="v\$(date +%Y%m%d%H%M)"
              gs-auto set-output image "registry.example.com/acme/billing:\$TAG"
        - id: deploy
          uses: /acme/platform/deploy-service@v1.0.0
          with:
            service: billing
            env: prod
            image: \${{ steps.build.outputs.image }}
        - id: notify
          if: always
          http:
            url: https://notify.example.com/releases
            headers:
              Authorization: "Bearer \${{ secrets.NOTIFY_TOKEN }}"
\`\`\`

Steps run in order with \`if: success\` by default, or \`if: failure\` / \`if: always\` for cleanup and alerts. Canceling a pipeline automatically cancels any active child step, and \`gs auto logs <run-id> --follow\` streams each step's logs in sequence while \`gs auto show <run-id>\` (and the web UI) displays per-step status, template provenance, and outputs.

## 5. Try before you land

You can test automations before submitting changes to \`main\`:

\`\`\`bash
# Run an automation as defined in an open draft changeset
gs auto run deploy-staging --from-changeset <id> --follow

# Or run a worker script locally from your workspace with stand-in secrets
gs auto run my-worker --local --secret API_TOKEN=test-value
\`\`\`

## 6. Get started

Everything is available now in the web UI (under any folder's **Policy** drawer) and in the \`gs\` CLI:

\`\`\`bash
gs auto ls [folder]
gs auto inspect /<account>/<folder>/<template>[@<tag>]
gs auto run <id> [-i key=value]... [--yes] --follow
gs auto show <run-id>
\`\`\`

For the full schema reference, egress tiers, container runtimes, worker SDK API, reusable template rules, and pipeline examples, read the [Automations docs](/doc/automations).`;
