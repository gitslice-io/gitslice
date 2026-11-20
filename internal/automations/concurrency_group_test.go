package automations

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

func TestParseConcurrencyGroup(t *testing.T) {
	file, err := Parse([]byte(`
version: 1
automations:
  deploy:
    on: [manual]
    concurrency_group: prod-server
    action:
      worker: ops/runbook.js
`))
	if err != nil {
		t.Fatal(err)
	}
	got := file.Automations["deploy"]
	if got.ConcurrencyGroup != "prod-server" || got.Concurrency != ConcurrencySerial {
		t.Fatalf("group %q concurrency %q, want prod-server serial", got.ConcurrencyGroup, got.Concurrency)
	}

	for name, body := range map[string]string{
		"bad name":     "concurrency_group: \"has space\"\n    action:\n      worker: a.js",
		"not serial":   "concurrency_group: g\n    concurrency: parallel\n    action:\n      worker: a.js",
		"http action":  "concurrency_group: g\n    action:\n      http:\n        url: https://example.com/hook",
		"check action": "concurrency_group: g\n    action:\n      check: [test]",
	} {
		_, err := Parse([]byte("version: 1\nautomations:\n  a:\n    on: [manual]\n    " + body + "\n"))
		if err == nil || !strings.Contains(err.Error(), "concurrency_group") {
			t.Errorf("%s: err = %v, want a concurrency_group error", name, err)
		}
	}
}

func TestMustWaitTakesTurnsOldestFirst(t *testing.T) {
	now := time.Now()
	older := storage.AutomationRun{ID: "trun_a", Status: storage.AutomationRunQueued, CreatedAt: now.Add(-time.Minute)}
	run := storage.AutomationRun{ID: "trun_b", Status: storage.AutomationRunQueued, CreatedAt: now}
	newer := storage.AutomationRun{ID: "trun_c", Status: storage.AutomationRunQueued, CreatedAt: now.Add(time.Minute)}
	running := storage.AutomationRun{ID: "trun_d", Status: storage.AutomationRunWaiting, CreatedAt: now.Add(time.Hour)}
	sameTime := storage.AutomationRun{ID: "trun_0", Status: storage.AutomationRunQueued, CreatedAt: now}

	cases := []struct {
		name string
		open []storage.AutomationRun
		want bool
	}{
		{"alone", []storage.AutomationRun{run}, false},
		{"only newer queued", []storage.AutomationRun{run, newer}, false},
		{"older queued", []storage.AutomationRun{older, run}, true},
		{"another one waiting for callbacks", []storage.AutomationRun{run, running}, true},
		{"same time, lower id first", []storage.AutomationRun{run, sameTime}, true},
	}
	for _, c := range cases {
		if got := mustWait(run, c.open); got != c.want {
			t.Errorf("%s: mustWait = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestListReportsLegacyTriggersFile(t *testing.T) {
	tree := mapTree{
		"/acme/web/.gitslice/triggers.yaml": "version: 1\ntriggers: {}\n",
	}
	_, problems, err := List(context.Background(), tree, "root", "acme", "/acme/web")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || problems[0].File != "/acme/web/.gitslice/triggers.yaml" || !strings.Contains(problems[0].Message, "rename it to .gitslice/automations.yaml") {
		t.Fatalf("problems = %+v, want one about renaming triggers.yaml", problems)
	}

	tree["/acme/web/.gitslice/automations.yaml"] = "version: 1\nautomations: {}\n"
	if _, problems, _ := List(context.Background(), tree, "root", "acme", "/acme/web"); len(problems) != 0 {
		t.Fatalf("with automations.yaml present, problems = %+v, want none", problems)
	}
}

func TestLeaseForCoversBashTimeout(t *testing.T) {
	bash := RunSpec{Automation: Automation{Timeout: 10 * time.Minute, Action: Action{Kind: ActionBash}}}
	if got := LeaseFor(bash); got != 10*time.Minute+RunLease {
		t.Fatalf("bash lease = %v", got)
	}
	worker := RunSpec{Automation: Automation{Timeout: time.Hour, Action: Action{Kind: ActionWorker}}}
	if got := LeaseFor(worker); got != RunLease {
		t.Fatalf("worker lease = %v, want RunLease", got)
	}
}
