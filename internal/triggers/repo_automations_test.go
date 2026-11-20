package triggers

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
		if len(file.Triggers) == 0 {
			t.Fatalf("%s defines no automations", p)
		}
	}
}

// TestRepositoryDeployRunbooksMatchOpsBuild keeps the deployment runbooks
// (automations posting to OPS_WEBHOOK_URL) manual-only, limited to writers
// and admins, and each handled by ops/automation/cloudbuild.yaml, which runs
// only the operations it names.
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
	for name, a := range file.Triggers {
		if !slices.Contains(a.Secrets, "OPS_WEBHOOK_URL") && !slices.Contains(a.Secrets, "OPS_WEBHOOK_QUERY") {
			continue
		}
		runbooks++
		if !slices.Equal(a.On, []string{EventManual}) {
			t.Errorf("%s: on = %v, want [manual] only", name, a.On)
		}
		if a.Manual == nil || len(a.Manual.Roles) == 0 {
			t.Errorf("%s: needs manual.roles", name)
		}
		// Either the worker .gitslice/ops/runbook.js (which waits for the
		// build's report) or a plain post to the webhook.
		if a.Action.Worker != "ops/runbook.js" && (a.Action.HTTP == nil || a.Action.HTTP.URL != "${{ secrets.OPS_WEBHOOK_URL }}") {
			t.Errorf("%s: action must be worker ops/runbook.js or post to OPS_WEBHOOK_URL", name)
		}
		if a.Action.Worker != "" && (!slices.Contains(a.Network, "cloudbuild.googleapis.com") || !slices.Contains(a.Secrets, "OPS_WEBHOOK_QUERY")) {
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
