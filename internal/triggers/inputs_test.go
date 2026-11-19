package triggers

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseManualInputsAndPolicy(t *testing.T) {
	file, err := Parse([]byte(`version: 1
automations:
  deploy:
    description: Deploy staging service
    on: [manual]
    manual:
      roles: [admin]
      require_human: true
      confirm: "Deploy billing to staging?"
    inputs:
      version:
        type: string
        required: true
        description: Release tag
        pattern: "^v[0-9]+\\.[0-9]+$"
      region:
        type: choice
        options: [us-central1, europe-west1]
        default: us-central1
      dry_run:
        type: boolean
        default: false
      replicas:
        type: number
        default: 3
    secrets: [CF_TOKEN]
    network: [api.cloudflare.com]
    action:
      bash:
        runtime: cloud
        size: standard-1
        checkout: folder
        include: [/acme/shared/scripts]
        working_dir: deploy
        env:
          APP_ENV: staging
        run: |
          echo "Deploying $INPUT_VERSION"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	deploy := file.Triggers["deploy"]
	if deploy.Description != "Deploy staging service" {
		t.Fatalf("description = %q", deploy.Description)
	}
	if deploy.Manual == nil || deploy.Manual.AllowsWriter() || !deploy.Manual.RequireHuman || deploy.Manual.Confirm != "Deploy billing to staging?" {
		t.Fatalf("manual = %+v", deploy.Manual)
	}
	if deploy.Egress != EgressHTTPGate {
		t.Fatalf("egress = %q, want http-gate", deploy.Egress)
	}
	if deploy.Timeout != DefaultBashTimeout {
		t.Fatalf("timeout = %v, want %v", deploy.Timeout, DefaultBashTimeout)
	}
	if deploy.Action.Kind != ActionBash || deploy.Action.Bash == nil {
		t.Fatalf("action = %+v", deploy.Action)
	}
	b := deploy.Action.Bash
	if b.Runtime != BashRuntimeCloud || b.Size != BashSizeStandard1 || b.Checkout != BashCheckoutFolder || b.WorkingDir != "deploy" {
		t.Fatalf("bash = %+v", b)
	}

	// ValidateInputs fills defaults and validates types/patterns.
	got, err := ValidateInputs(deploy.Inputs, map[string]string{"version": "v1.2"})
	if err != nil {
		t.Fatalf("ValidateInputs valid: %v", err)
	}
	want := map[string]string{"version": "v1.2", "region": "us-central1", "dry_run": "false", "replicas": "3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ValidateInputs = %+v, want %+v", got, want)
	}

	// Error cases for ValidateInputs.
	for name, bad := range map[string]map[string]string{
		"missing required": {},
		"pattern mismatch": {"version": "latest"},
		"bad choice":       {"version": "v1.0", "region": "ap-south1"},
		"bad bool":         {"version": "v1.0", "dry_run": "maybe"},
		"bad number":       {"version": "v1.0", "replicas": "many"},
		"unknown key":      {"version": "v1.0", "extra": "1"},
	} {
		if _, err := ValidateInputs(deploy.Inputs, bad); err == nil {
			t.Fatalf("ValidateInputs(%s) = nil error", name)
		}
	}
}

func TestParseBashEgressAndValidation(t *testing.T) {
	// Offline bash (default when network omitted) + changeset-only default checkout: none.
	file, err := Parse([]byte(`version: 1
automations:
  lint:
    on: [changeset.updated]
    action:
      bash:
        script: ./scripts/lint.sh
  migrate:
    on: [manual]
    secrets: [DB_URL]
    reveal: [DB_URL]
    egress: direct
    timeout: 15m
    action:
      bash:
        runtime: cloud
        run: psql "$DB_URL"
`))
	if err != nil {
		t.Fatalf("Parse valid bash: %v", err)
	}
	lint := file.Triggers["lint"]
	if lint.Egress != EgressOffline || lint.Action.Bash.Checkout != BashCheckoutNone || lint.Action.Bash.Script != "scripts/lint.sh" {
		t.Fatalf("lint = %+v bash=%+v", lint, lint.Action.Bash)
	}
	migrate := file.Triggers["migrate"]
	if migrate.Egress != EgressDirect || migrate.Action.Bash.Checkout != BashCheckoutFolder || migrate.Timeout != 15*time.Minute {
		t.Fatalf("migrate = %+v bash=%+v", migrate, migrate.Action.Bash)
	}

	for name, bad := range map[string]string{
		"direct without reveal": `version: 1
automations:
  a:
    on: [manual]
    secrets: [DB_URL]
    egress: direct
    action: {bash: {run: "echo hi"}}`,
		"direct with network": `version: 1
automations:
  a:
    on: [manual]
    network: [api.example.com]
    egress: direct
    action: {bash: {run: "echo hi"}}`,
		"http-gate without network": `version: 1
automations:
  a:
    on: [manual]
    egress: http-gate
    action: {bash: {run: "echo hi"}}`,
		"offline with network": `version: 1
automations:
  a:
    on: [manual]
    network: [api.example.com]
    egress: offline
    action: {bash: {run: "echo hi"}}`,
		"egress on worker": `version: 1
automations:
  a:
    on: [manual]
    egress: direct
    action: {worker: a.js}`,
		"bash timeout too high": `version: 1
automations:
  a:
    on: [manual]
    timeout: 20m
    action: {bash: {run: "echo hi"}}`,
		"bash both run and script": `version: 1
automations:
  a:
    on: [manual]
    action: {bash: {run: "echo hi", script: "a.sh"}}`,
		"bash escaping script": `version: 1
automations:
  a:
    on: [manual]
    action: {bash: {script: "../evil.sh"}}`,
		"bash bad runtime": `version: 1
automations:
  a:
    on: [manual]
    action: {bash: {runtime: rust, run: "echo hi"}}`,
		"bash bad size": `version: 1
automations:
  a:
    on: [manual]
    action: {bash: {size: huge, run: "echo hi"}}`,
		"bash reserved env": `version: 1
automations:
  a:
    on: [manual]
    action: {bash: {run: "echo hi", env: {INPUT_X: "1"}}}`,
		"bash env collides with secret": `version: 1
automations:
  a:
    on: [manual]
    secrets: [API_KEY]
    network: [api.example.com]
    action: {bash: {run: "echo hi", env: {API_KEY: "override"}}}`,
		"inputs without manual": `version: 1
automations:
  a:
    on: [push]
    inputs:
      env: {type: string}
    action: {bash: {run: "echo hi"}}`,
		"manual policy without manual": `version: 1
automations:
  a:
    on: [push]
    manual: {require_human: true}
    action: {bash: {run: "echo hi"}}`,
		"input env collision": `version: 1
automations:
  a:
    on: [manual]
    inputs:
      dry-run: {type: boolean}
      dry_run: {type: boolean}
    action: {bash: {run: "echo hi"}}`,
		"choice without options": `version: 1
automations:
  a:
    on: [manual]
    inputs:
      env: {type: choice}
    action: {bash: {run: "echo hi"}}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("Parse(%s) = nil error", name)
		}
	}
}

func TestInputEnvName(t *testing.T) {
	if got := InputEnvName("dry-run"); got != "INPUT_DRY_RUN" {
		t.Fatalf("InputEnvName = %q", got)
	}
	if !strings.HasPrefix(InputEnvName("region"), "INPUT_") {
		t.Fatalf("unexpected prefix")
	}
}
