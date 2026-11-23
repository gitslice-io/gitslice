# 38. Reusable Automation Templates & Multi-Step Pipelines

> **Status:** Implemented (2026-11-21). Extends `design/37_automations_and_containers.md`.

## Goals

1. **No separate registry or package manager:** Every folder in Gitslice's global
   monorepo can already publish reusable automation templates via
   `.gitslice/automations.yaml` and version them with immutable slice tags
   (`@v1.2.0`) or commit SHAs (`@sha256:...`).
2. **Caller-bounded security:** Calling a template (especially cross-account)
   must never grant the template access to secrets, plaintext `reveal:`,
   `egress: direct`, unapproved `network:` hosts, or longer timeouts than the
   caller explicitly authorized.
3. **Composable multi-step workflows:** Sequential pipelines (`action.steps`)
   let teams chain reusable templates and inline actions, pass named outputs
   downstream, and run `failure`/`always` steps for cleanup or notifications.

---

## Phase 1: Reusable Templates (`on: [call]` + `action.uses`)

### Defining a Template

Any folder's `.gitslice/automations.yaml` can declare a reusable template with
`on: [call]`:

- Templates use the same schema (`description`, `inputs`, `secrets`, `reveal`,
  `network`, `egress`, `timeout`, `action: { bash | worker | http | check }`).
- A template may also include other events (e.g., `on: [call, manual]`).
- **No recursive composition:** An `on: [call]` template cannot use
  `action: uses` or `action: steps` (rejected at parse time).

### Calling a Template (`action.uses`)

```yaml
secrets:
  GCP_SA_KEY: BILLING_PROD_GCP_KEY   # template slot -> caller folder secret
reveal: [BILLING_PROD_GCP_KEY]       # explicit caller opt-in
egress: direct                       # explicit caller opt-in
action:
  uses: /acme/platform/deploy-cloudrun@v1.2.0
  with:
    service: billing-api
```

- `secrets:` accepts either a YAML list (`[FOO]`, binding `FOO -> FOO`) or a
  YAML mapping (`{ TEMPLATE_SLOT: CALLER_SECRET }`).
- `uses` reference grammar (`internal/automations/templates.go:ParseUsesRef`):
  `/<account>[/<folder>...]/<name>[@<tag>|@sha256:<commit>]`.
- **Pinning rules (`service/automation_templates.go:sliceTemplateLookup`):**
  - Same-account unpinned refs resolve live from the caller's target tree.
  - `@sha256:<commit>` resolves directly from that commit's tree (must be public
    for cross-account callers).
  - `@<tag>` resolves via immutable slice tags covering the template folder
    (cross-account requires a public slice).
  - Cross-account unpinned refs are rejected.

### Caller-Bounded Capability Ceilings (`BindTemplate`)

When resolving `action.uses` into an executable `Automation`:

1. **Secrets:** Every secret slot declared by the template must be bound by the
   caller. Values are always read from the **caller's folder** (`run.Folder`),
   never the template's folder, and remapped to template slot names before
   dispatch (`service/automation_run.go:startResponse`).
2. **Reveal:** If the template lists slot `S` in `reveal:`, the caller must
   explicitly list the mapped caller secret name in the caller's `reveal:`.
3. **Egress:** If the template requires `egress: direct`, the caller must
   explicitly set `egress: direct`.
4. **Network:**
   - Cross-account callers must explicitly allow every host in the template's
     `network:` allowlist (supports exact match or wildcard coverage).
   - Same-account callers may omit `network:` to inherit the template's
     allowlist, or specify a `network:` allowlist that must cover the template's
     hosts.
5. **Timeout:** If the caller explicitly sets `timeout:`, it acts as an upper
   bound (`min(caller.Timeout, template.Timeout)`).
6. **Code provenance:** Scripts (`bash.script`, `worker` modules) are loaded
   from `(TemplateTreeID, TemplateFolder)` while repository checkouts
   (`bash.checkout`) still inspect the caller's folder and commit. Every run
   records `template_id` and `template_commit_id`.
7. **Input inheritance (`EffectiveInputsForUses`):** Any template input not
   bound in `with:` automatically inherits into the caller's effective `inputs:`
   schema in `ListAutomations`, `GetFolderPolicy`, `gs auto run`, and
   `RunAutomationModal`.

### Template Discovery (`InspectAutomationTemplate` / `gs auto inspect`)

`AutomationService.InspectAutomationTemplate` resolves a `uses` reference and
returns the template's `AutomationDefinition`, resolved account/folder/commit,
canonical `resolved_ref`, and available immutable slice tags covering that
folder. Exposed in CLI via `gs auto inspect <uses-ref> [--account <account>]`.

---

## Phase 2: Multi-Step Pipelines (`action.steps`) & Step Outputs

### Pipeline YAML

```yaml
action:
  steps:
    - id: build
      bash: { runtime: go, checkout: folder, run: "gs-auto set-output image gcr.io/acme/app:v1" }
    - id: deploy
      uses: /acme/platform/deploy-cloudrun@v1.2.0
      with: { service: app, image: "${{ steps.build.outputs.image }}" }
    - id: notify
      if: always
      http: { url: "https://hooks.slack.com/services/${{ secrets.SLACK_WEBHOOK }}" }
```

- Up to `MaxSteps = 20` sequential steps.
- Each step has an optional `id` (defaults to `step_<n>`), optional `name`,
  `if: success` (default) | `failure` | `always`, optional `timeout`, and one
  action (`uses`, `bash`, `worker`, `http`, or `check`).

### Parent / Child Run Model

Migration `0066_reusable_automations_and_pipelines.sql` adds `template_id`,
`template_commit_id`, `parent_run_id`, `step_index`, `step_id`, and `outputs`
(`jsonb`) plus `trigger_runs_parent_idx` to `trigger_runs`.

- Top-level `ListAutomationRuns` excludes child runs (`parent_run_id = ''`).
  Passing `parent_run_id` returns child runs ordered by `step_index ASC`.
- A pipeline run executes as a parent `AutomationRun` (`action = "steps"`,
  `status = "waiting"`). `Engine.runPipeline` starts step 0 as a normal child
  `AutomationRun` dispatched through the existing `http`/`worker`/`bash`/`check`
  paths.
- Whenever any child run transitions to a terminal state (`FinishRun`, `Cancel`,
  `settleExpired`, HTTP completion, or check/worker error), `Engine.OnRunFinished`
  advances the parent pipeline idempotently:
  - Evaluates the next step's `if:` against whether any prior step failed.
  - Interpolates `${{ inputs.<name> }}` and `${{ steps.<id>.outputs.<key> }}`
    into step `with:`, `bash.env`, `bash.run`, and `http` URL/headers.
  - Enqueues the next child run, or marks the parent `succeeded`/`failed` once
    all steps complete.
- Canceling or timing out a parent pipeline cascades cancellation to all
  non-terminal child runs (`cancelPipelineChildren`).

### Named Run Outputs (`SetRunOutput`)

- **Worker SDK:** `await gs.setOutput(key, value)` → `AutomationRunService.SetRunOutput`.
- **Container helper:** `gs-auto set-output <key> <value>` → `POST http://gitslice.internal/gs/output/set` → `SetRunOutput` (plus `gs-auto set-details-url <url>` → `SetRunDetailsUrl`).
- **Validation & masking:** Keys must match `^[A-Za-z_][A-Za-z0-9_-]{0,63}$`,
  max 50 outputs per run, max 16 KiB per value. Both the dispatcher bridge and
  the server scrub every secret value visible at the run's folder before storing
  the output.
