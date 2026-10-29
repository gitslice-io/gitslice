package checks

import (
	"context"
	"testing"
)

// A check with started_by: trigger matches like any other but is not started
// with the patchset; only a trigger starts it (design/34_triggers.md).
func TestStartedByTrigger(t *testing.T) {
	file, err := Parse([]byte(`version: 1
defaults:
  started_by: trigger
checks:
  unit:
    run: go test ./...
  lint:
    run: golangci-lint run
    started_by: patchset
  docs:
    run: echo docs
    paths: ["**/*.md"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if file.Checks["unit"].StartedBy != StartedByTrigger || file.Checks["lint"].StartedBy != StartedByPatchset {
		t.Fatalf("started_by = %q, %q", file.Checks["unit"].StartedBy, file.Checks["lint"].StartedBy)
	}
	if _, err := Parse([]byte("version: 1\nchecks:\n  a:\n    run: x\n    started_by: someone\n")); err == nil {
		t.Fatal("an unknown started_by parsed")
	}

	tree := fakeTreeReader{"svc/.gitslice/checks.yaml": []byte(`version: 1
checks:
  unit:
    run: go test ./...
    started_by: trigger
  lint:
    run: golangci-lint run
  docs:
    run: echo docs
    paths: ["**/*.md"]
    started_by: trigger
`)}
	plan, err := ResolvePlan(context.Background(), tree, "root", []string{"svc/main.go"}, []string{"/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Runnable) != 1 || plan.Runnable[0].Name != "svc/lint" {
		t.Fatalf("runnable = %+v", plan.Runnable)
	}
	if len(plan.Triggered) != 1 || plan.Triggered[0].Name != "svc/unit" {
		t.Fatalf("triggered = %+v", plan.Triggered)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].Name != "svc/docs" {
		t.Fatalf("skipped = %+v", plan.Skipped)
	}
	if _, ok := plan.Spec("svc/unit"); !ok {
		t.Fatal("Spec does not find a triggered check")
	}
}
