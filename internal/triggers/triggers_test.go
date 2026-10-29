package triggers

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type mapTree map[string]string

func (m mapTree) ReadFile(_ context.Context, _ string, p string) ([]byte, error) {
	data, ok := m[p]
	if !ok {
		return nil, ErrNotFound
	}
	return []byte(data), nil
}

const billingTriggers = `
version: 1
triggers:
  notify:
    on: [push]
    refs: [main]
    paths: ["**/*.go", "!**/*_test.go"]
    secrets: [DEPLOY_TOKEN, DEPLOY_HMAC]
    action:
      http:
        url: https://deploy.example.com/hook
        headers:
          Authorization: "Bearer ${{ secrets.DEPLOY_TOKEN }}"
        sign: DEPLOY_HMAC
  ci:
    on: [changeset.created, changeset.updated]
    paths: ["**/*.go", "/go.mod"]
    changes: [added, modified, moved]
    checks: [ci]
    secrets: [BUILDKITE_TOKEN]
    reveal: [RUN_TOKEN]
    network: [api.buildkite.com, "*.example.com"]
    timeout: 2d
    retry: 2
    action:
      worker: ./triggers/buildkite.js
`

func TestParse(t *testing.T) {
	file, err := Parse([]byte(billingTriggers))
	if err != nil {
		t.Fatal(err)
	}
	notify, ci := file.Triggers["notify"], file.Triggers["ci"]
	if notify.Action.Kind != ActionHTTP || notify.Action.HTTP.Sign != "DEPLOY_HMAC" || notify.Concurrency != ConcurrencyParallel || notify.Timeout != DefaultTimeout {
		t.Fatalf("notify = %+v", notify)
	}
	if ci.Action.Kind != ActionWorker || ci.Action.Worker != "triggers/buildkite.js" || ci.Concurrency != ConcurrencyCancelSuperseded ||
		ci.Timeout != 48*time.Hour || ci.Retry != 2 || !reflect.DeepEqual(ci.Network, []string{"api.buildkite.com", "*.example.com"}) {
		t.Fatalf("ci = %+v", ci)
	}

	for name, bad := range map[string]string{
		"no version":        "triggers: {}",
		"unknown field":     "version: 1\ntriggers:\n  a:\n    on: [push]\n    nope: 1\n    action: {worker: a.js}",
		"bad event":         "version: 1\ntriggers:\n  a:\n    on: [pushed]\n    action: {worker: a.js}",
		"schedule":          "version: 1\ntriggers:\n  a:\n    on: [schedule]\n    action: {worker: a.js}",
		"two actions":       "version: 1\ntriggers:\n  a:\n    on: [push]\n    action: {worker: a.js, http: {url: 'https://x.io'}}",
		"escaping script":   "version: 1\ntriggers:\n  a:\n    on: [push]\n    action: {worker: ../a.js}",
		"not js":            "version: 1\ntriggers:\n  a:\n    on: [push]\n    action: {worker: a.py}",
		"ftp url":           "version: 1\ntriggers:\n  a:\n    on: [push]\n    action: {http: {url: 'ftp://x.io'}}",
		"unlisted secret":   "version: 1\ntriggers:\n  a:\n    on: [push]\n    action: {http: {url: 'https://x.io/${{ secrets.T }}'}}",
		"reveal unlisted":   "version: 1\ntriggers:\n  a:\n    on: [push]\n    reveal: [T]\n    action: {worker: a.js}",
		"bad host":          "version: 1\ntriggers:\n  a:\n    on: [push]\n    network: ['10.0.0.1']\n    action: {worker: a.js}",
		"network on http":   "version: 1\ntriggers:\n  a:\n    on: [push]\n    network: [x.io]\n    action: {http: {url: 'https://x.io'}}",
		"long timeout":      "version: 1\ntriggers:\n  a:\n    on: [push]\n    timeout: 8d\n    action: {worker: a.js}",
		"retry too high":    "version: 1\ntriggers:\n  a:\n    on: [push]\n    retry: 6\n    action: {worker: a.js}",
		"bad change kind":   "version: 1\ntriggers:\n  a:\n    on: [push]\n    changes: [renamed]\n    action: {worker: a.js}",
		"bad trigger name":  "version: 1\ntriggers:\n  a b:\n    on: [push]\n    action: {worker: a.js}",
		"glob with ascent":  "version: 1\ntriggers:\n  a:\n    on: [push]\n    paths: ['../x']\n    action: {worker: a.js}",
		"header line break": "version: 1\ntriggers:\n  a:\n    on: [push]\n    action: {http: {url: 'https://x.io', headers: {X: \"a\\nb\"}}}",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("Parse(%s) = nil error", name)
		}
	}
}

func TestResolve(t *testing.T) {
	tree := mapTree{
		"/acme/services/billing/.gitslice/triggers.yaml": billingTriggers,
		"/acme/.gitslice/triggers.yaml": `
version: 1
triggers:
  everything:
    on: [push, tag.created]
    action: {worker: all.js}
`,
		"/acme/services/.gitslice/triggers.yaml": "version: 1\ntriggers: [",
	}
	ctx := context.Background()

	push := Event{Kind: EventPush, Account: "acme", Ref: "refs/global/main", Changes: []Change{
		{Path: "/acme/services/billing/api/pay.go", Kind: ChangeModified},
		{Path: "/acme/services/billing/api/pay_test.go", Kind: ChangeModified},
		{Path: "/acme/web/index.ts", Kind: ChangeAdded},
	}}
	matches, problems, err := Resolve(ctx, tree, "root", push)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(matches); !reflect.DeepEqual(got, []string{"everything", "services/billing/notify"}) {
		t.Fatalf("push matches = %v", got)
	}
	if len(matches[1].Changes) != 1 || matches[1].Changes[0].Path != "/acme/services/billing/api/pay.go" {
		t.Fatalf("notify matched %v, want pay.go only (tests excluded)", matches[1].Changes)
	}
	if len(problems) != 1 || problems[0].Folder != "/acme/services" {
		t.Fatalf("problems = %+v", problems)
	}

	// Another ref: notify (refs: [main]) does not fire.
	push.Ref = "refs/global/release"
	matches, _, _ = Resolve(ctx, tree, "root", push)
	if got := ids(matches); !reflect.DeepEqual(got, []string{"everything"}) {
		t.Fatalf("push to another ref = %v", got)
	}

	// changes: a deletion alone does not start ci; an absolute glob matches
	// /go.mod only when it is under the trigger's folder.
	cs := Event{Kind: EventChangesetUpdated, Account: "acme", Ref: "refs/global/main", Changes: []Change{
		{Path: "/acme/services/billing/old.go", Kind: ChangeDeleted},
	}}
	if matches, _, _ = Resolve(ctx, tree, "root", cs); len(matches) != 0 {
		t.Fatalf("deletion matches = %v", ids(matches))
	}
	cs.Changes = append(cs.Changes, Change{Path: "/acme/services/billing/new.go", Kind: ChangeMoved, OldPath: "/acme/services/billing/old2.go"})
	if matches, _, _ = Resolve(ctx, tree, "root", cs); !reflect.DeepEqual(ids(matches), []string{"services/billing/ci"}) {
		t.Fatalf("move matches = %v", ids(matches))
	}
	// A move into the folder from outside matches by its new path.
	moved := Event{Kind: EventChangesetCreated, Account: "acme", Changes: []Change{{Path: "/acme/services/billing/x.go", Kind: ChangeMoved, OldPath: "/acme/lib/x.go"}}}
	if matches, _, _ = Resolve(ctx, tree, "root", moved); !reflect.DeepEqual(ids(matches), []string{"services/billing/ci"}) {
		t.Fatalf("moved-in matches = %v", ids(matches))
	}

	// A changeset event with no files yet fires nothing.
	if matches, _, _ = Resolve(ctx, tree, "root", Event{Kind: EventChangesetCreated, Account: "acme", Folders: []string{"/acme/services/billing"}}); len(matches) != 0 {
		t.Fatalf("empty changeset matches = %v", ids(matches))
	}
	// A tag fires the triggers in the tagging slice's folders and above.
	tag := Event{Kind: EventTagCreated, Account: "acme", Folders: []string{"/acme/web"}}
	if matches, _, _ = Resolve(ctx, tree, "root", tag); !reflect.DeepEqual(ids(matches), []string{"everything"}) {
		t.Fatalf("tag matches = %v", ids(matches))
	}

	// A manual run picks one trigger by id.
	manual := Event{Kind: EventManual, Account: "acme", Only: "services/billing/ci"}
	if matches, _, _ = Resolve(ctx, tree, "root", manual); !reflect.DeepEqual(ids(matches), []string{"services/billing/ci"}) {
		t.Fatalf("manual matches = %v", ids(matches))
	}
}

func TestIDsAndFolders(t *testing.T) {
	if got := ID("/acme", "/acme", "x"); got != "x" {
		t.Fatalf("root id = %q", got)
	}
	if got := ID("/acme", "/acme/a/b", "x"); got != "a/b/x" {
		t.Fatalf("id = %q", got)
	}
	if got := FolderOf("acme", "a/b/x"); got != "/acme/a/b" {
		t.Fatalf("FolderOf = %q", got)
	}
	if got := CheckID("a/b/x", "ci"); got != "a/b/ci" {
		t.Fatalf("CheckID = %q", got)
	}
	if got := strings.Join(folderSpine("/acme", "/acme/a/b/f.go"), ","); got != "/acme,/acme/a,/acme/a/b" {
		t.Fatalf("folderSpine = %q", got)
	}
}

func TestChangesFromPatchset(t *testing.T) {
	got := ChangesFromPatchset(&corev1.Patchset{
		ChangedPaths: []string{"/a/new.go", "/a/old.go", "/a/kept.go", "/a/gone.go", "/a/moved.go", "/a/plain.go"},
		PathBases:    []*corev1.PathBase{{Path: "/a/new.go", Exists: false}, {Path: "/a/kept.go", Exists: true}},
		FileEdits: []*corev1.FileEdit{
			{Op: "upsert", Path: "/a/new.go"},
			{Op: "upsert", Path: "/a/kept.go"},
			{Op: "delete", Path: "/a/gone.go"},
			{Op: "rename", Path: "/a/moved.go", OldPath: "/a/old.go"},
		},
	})
	want := []Change{
		{Path: "/a/gone.go", Kind: ChangeDeleted},
		{Path: "/a/kept.go", Kind: ChangeModified},
		{Path: "/a/moved.go", Kind: ChangeMoved, OldPath: "/a/old.go"},
		{Path: "/a/new.go", Kind: ChangeAdded},
		{Path: "/a/plain.go", Kind: ChangeModified},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangesFromPatchset = %+v", got)
	}
}

func ids(matches []Match) []string {
	var out []string
	for _, m := range matches {
		out = append(out, m.ID)
	}
	return out
}

func TestParseCheckAction(t *testing.T) {
	file, err := Parse([]byte(`version: 1
triggers:
  ci:
    on: [changeset.created, changeset.updated]
    action:
      check: [test, vet, test]
`))
	if err != nil {
		t.Fatal(err)
	}
	ci := file.Triggers["ci"]
	if ci.Action.Kind != ActionCheck || !reflect.DeepEqual(ci.Action.Check, []string{"test", "vet"}) || !reflect.DeepEqual(ci.Checks, []string{"test", "vet"}) {
		t.Fatalf("ci = %+v", ci)
	}
	for name, bad := range map[string]string{
		"on push":     "version: 1\ntriggers:\n  a:\n    on: [push]\n    action: {check: [test]}",
		"no checks":   "version: 1\ntriggers:\n  a:\n    on: [changeset.updated]\n    action: {check: []}",
		"with secret": "version: 1\ntriggers:\n  a:\n    on: [changeset.updated]\n    secrets: [X]\n    action: {check: [test]}",
		"two kinds":   "version: 1\ntriggers:\n  a:\n    on: [changeset.updated]\n    action: {check: [test], worker: a.js}",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("Parse(%s) = nil error", name)
		}
	}
}
