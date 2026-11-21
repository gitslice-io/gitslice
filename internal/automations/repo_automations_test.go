package automations

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// TestRepositoryAutomationsFilesParse keeps the repository's own automations
// files valid.
func TestRepositoryAutomationsFilesParse(t *testing.T) {
	for _, p := range []string{"../../.gitslice/automations.yaml", "../../web/.gitslice/automations.yaml"} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		file, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if len(file.Automations) == 0 {
			t.Fatalf("%s defines no automations", p)
		}
	}
}

// TestRepositoryDeployRunbooksMatchOpsBuild keeps the deployment runbooks
// (automations posting to the ops webhook) manual-only, limited to writers
// and admins, and each handled by ops/automation/cloudbuild.yaml, which runs
// only the operations it names. The one exception is release, which runs on
// tag.created (only writers create tags).
func TestRepositoryDeployRunbooksMatchOpsBuild(t *testing.T) {
	data, err := os.ReadFile("../../.gitslice/automations.yaml")
	if err != nil {
		t.Fatal(err)
	}
	file, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	build, err := os.ReadFile("../../ops/automation/cloudbuild.yaml")
	if err != nil {
		t.Fatal(err)
	}
	runbooks := 0
	for name, a := range file.Automations {
		if !slices.Contains(a.Secrets, "OPS_WEBHOOK_URL") && !slices.Contains(a.Secrets, "OPS_WEBHOOK_QUERY") {
			continue
		}
		runbooks++
		if name == "release" {
			if !slices.Equal(a.On, []string{EventTagCreated}) {
				t.Errorf("%s: on = %v, want [tag.created] only", name, a.On)
			}
		} else {
			if !slices.Equal(a.On, []string{EventManual}) {
				t.Errorf("%s: on = %v, want [manual] only", name, a.On)
			}
			if a.Manual == nil || len(a.Manual.Roles) == 0 {
				t.Errorf("%s: needs manual.roles", name)
			}
		}
		// The worker .gitslice/ops/runbook.js posts to the webhook and waits
		// for the build's report.
		if a.Action.Worker != "ops/runbook.js" {
			t.Errorf("%s: action must be worker ops/runbook.js", name)
		}
		if !slices.Contains(a.Network, "cloudbuild.googleapis.com") || !slices.Contains(a.Secrets, "OPS_WEBHOOK_QUERY") {
			t.Errorf("%s: the worker needs network [cloudbuild.googleapis.com] and secret OPS_WEBHOOK_QUERY", name)
		}
		if !strings.Contains(string(build), "gitslice/"+name+")") {
			t.Errorf("%s: ops/automation/cloudbuild.yaml has no case for gitslice/%s", name, name)
		}
	}
	if runbooks == 0 {
		t.Fatal("no deployment runbooks found")
	}
}

func TestRepositoryCloudflareRunbooksMatchScript(t *testing.T) {
	data, err := os.ReadFile("../../.gitslice/automations.yaml")
	if err != nil {
		t.Fatal(err)
	}
	file, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../../.gitslice/ops/cloudflare.sh")
	if err != nil {
		t.Fatal(err)
	}
	runbooks := 0
	for name, a := range file.Automations {
		if !slices.Contains(a.Secrets, "CLOUDFLARE_API_TOKEN") {
			continue
		}
		runbooks++
		if a.Action.Bash == nil || a.Action.Bash.Script == "" {
			t.Errorf("%s: must be a bash action running ops/cloudflare.sh", name)
			continue
		}
		if op := a.Action.Bash.Env["OP"]; op != name || !strings.Contains(string(script), "\n  "+op+")") {
			t.Errorf("%s: env OP = %q; it must be the automation's name, with a case in ops/cloudflare.sh", name, op)
		}
		if a.Manual == nil || len(a.Manual.Roles) == 0 {
			t.Errorf("%s: needs manual.roles", name)
		}
		// Web deploys also run on each landing that changes web/; the
		// others are manual-only.
		if strings.HasPrefix(name, "deploy-web") {
			if !slices.Equal(a.On, []string{EventPush, EventManual}) || !slices.Equal(a.Paths, []string{"web/**"}) {
				t.Errorf("%s: on = %v paths = %v, want [push manual] on web/**", name, a.On, a.Paths)
			}
		} else if !slices.Equal(a.On, []string{EventManual}) {
			t.Errorf("%s: must be manual-only", name)
		}
		want := "prod-workers"
		if strings.HasSuffix(name, "-staging") {
			want = "staging-workers"
		}
		if !strings.HasSuffix(name, "-selftest") && a.ConcurrencyGroup != want {
			t.Errorf("%s: concurrency_group = %q, want %s", name, a.ConcurrencyGroup, want)
		}
	}
	if runbooks == 0 {
		t.Fatal("no Cloudflare runbooks found")
	}
}
