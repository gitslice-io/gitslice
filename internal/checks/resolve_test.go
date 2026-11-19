package checks

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeTreeReader map[string][]byte

func (f fakeTreeReader) ReadFile(_ context.Context, _, path string) ([]byte, error) {
	data, ok := f[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func TestResolvePlan(t *testing.T) {
	tests := []struct {
		name           string
		files          fakeTreeReader
		changedPaths   []string
		includedPaths  []string
		requiredChecks []string
		assert         func(t *testing.T, plan *Plan)
	}{
		{
			name: "qualified id cascade",
			files: fakeTreeReader{
				"/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  test:
    run: root-test
`),
				"backend/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  test:
    run: backend-test
`),
			},
			changedPaths:  []string{"backend/app.go"},
			includedPaths: []string{"/"},
			assert: func(t *testing.T, plan *Plan) {
				wantNames := []string{"test", "backend/test"}
				if got := runnableNames(plan); !reflect.DeepEqual(got, wantNames) {
					t.Fatalf("runnable names = %#v, want %#v", got, wantNames)
				}
			},
		},
		{
			name: "path filter skip",
			files: fakeTreeReader{
				"/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  test:
    run: go test ./...
    paths: ["**/*.go"]
`),
			},
			changedPaths:  []string{"README.md"},
			includedPaths: []string{"/"},
			assert: func(t *testing.T, plan *Plan) {
				if len(plan.Runnable) != 0 {
					t.Fatalf("Runnable = %#v, want none", plan.Runnable)
				}
				if got := skippedNames(plan); !reflect.DeepEqual(got, []string{"test"}) {
					t.Fatalf("skipped names = %#v, want [test]", got)
				}
			},
		},
		{
			name: "include union collapses nested prefixes",
			files: fakeTreeReader{
				"backend/service/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  test:
    run: go test ./...
    include:
      - "/backend"
      - "/backend/service/generated"
      - "/go.mod"
`),
			},
			changedPaths:  []string{"backend/service/app.go"},
			includedPaths: []string{"/"},
			assert: func(t *testing.T, plan *Plan) {
				requireRunnableCount(t, plan, 1)
				want := []string{"backend", "go.mod"}
				if got := plan.Runnable[0].MaterializePaths; !reflect.DeepEqual(got, want) {
					t.Fatalf("MaterializePaths = %#v, want %#v", got, want)
				}
			},
		},
		{
			name: "defaults merge and check override",
			files: fakeTreeReader{
				"backend/.gitslice/checks.yaml": []byte(`
version: 1
defaults:
  image: golang:1.22
  setup:
    - go env -w GOPROXY=off
  cache:
    - /go/pkg/mod
  timeout: 10m
  env:
    CGO_ENABLED: "0"
    SHARED: default
  network: true
  memory: 6g
  cpus: "3"
checks:
  test:
    run: go test ./...
    image: golang:1.23
    setup:
      - apt-get update
    cache:
      - /tmp/check-cache
    timeout: 30s
    working_dir: cmd/service
    env:
      SHARED: check
      EXTRA: "1"
    network: false
    memory: 8g
    cpus: "4"
`),
			},
			changedPaths:  []string{"backend/app.go"},
			includedPaths: []string{"/"},
			assert: func(t *testing.T, plan *Plan) {
				requireRunnableCount(t, plan, 1)
				spec := plan.Runnable[0]
				if spec.Image != "golang:1.23" {
					t.Fatalf("Image = %q", spec.Image)
				}
				if got, want := spec.Setup, []string{"apt-get update"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("Setup = %#v, want %#v", got, want)
				}
				if got, want := spec.Cache, []string{"/tmp/check-cache"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("Cache = %#v, want %#v", got, want)
				}
				if spec.Timeout != 30*time.Second {
					t.Fatalf("Timeout = %v, want 30s", spec.Timeout)
				}
				if spec.Network {
					t.Fatalf("Network = true, want false override")
				}
				if spec.Memory != "8g" {
					t.Fatalf("Memory = %q, want 8g", spec.Memory)
				}
				if spec.CPUs != "4" {
					t.Fatalf("CPUs = %q, want 4", spec.CPUs)
				}
				if spec.WorkingDir != "backend/cmd/service" {
					t.Fatalf("WorkingDir = %q", spec.WorkingDir)
				}
				wantEnv := map[string]string{
					"CGO_ENABLED": "0",
					"SHARED":      "check",
					"EXTRA":       "1",
				}
				if !reflect.DeepEqual(spec.Env, wantEnv) {
					t.Fatalf("Env = %#v, want %#v", spec.Env, wantEnv)
				}
			},
		},
		{
			name: "out of slice materialization",
			files: fakeTreeReader{
				"backend/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  test:
    run: go test ./...
    include: ["/go.mod"]
`),
			},
			changedPaths:  []string{"backend/app.go"},
			includedPaths: []string{"backend"},
			assert: func(t *testing.T, plan *Plan) {
				requireRunnableCount(t, plan, 1)
				if !plan.Runnable[0].OutOfSlice {
					t.Fatalf("OutOfSlice = false, want true")
				}
			},
		},
		{
			name: "missing required check",
			files: fakeTreeReader{
				"/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  lint:
    run: go vet ./...
`),
			},
			changedPaths:   []string{"README.md"},
			includedPaths:  []string{"/"},
			requiredChecks: []string{"test"},
			assert: func(t *testing.T, plan *Plan) {
				if got := erroredNames(plan); !reflect.DeepEqual(got, []string{"test"}) {
					t.Fatalf("errored names = %#v, want [test]", got)
				}
				if plan.Errored[0].Reason != "required check has no definition in this revision" {
					t.Fatalf("missing required reason = %q", plan.Errored[0].Reason)
				}
			},
		},
		{
			name: "required check outside the changed directories is skipped",
			files: fakeTreeReader{
				"/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  lint:
    run: go vet ./...
`),
				"web/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  build:
    run: npm run build
`),
			},
			changedPaths:   []string{"README.md"},
			includedPaths:  []string{"/"},
			requiredChecks: []string{"web/build"},
			assert: func(t *testing.T, plan *Plan) {
				if got := runnableNames(plan); !reflect.DeepEqual(got, []string{"lint"}) {
					t.Fatalf("runnable names = %#v, want [lint]", got)
				}
				if got := skippedNames(plan); !reflect.DeepEqual(got, []string{"web/build"}) {
					t.Fatalf("skipped names = %#v, want [web/build]", got)
				}
				if plan.Skipped[0].Reason != "no changed paths under web" {
					t.Fatalf("skipped reason = %q", plan.Skipped[0].Reason)
				}
				if len(plan.Errored) != 0 {
					t.Fatalf("errored = %#v, want none", plan.Errored)
				}
			},
		},
		{
			name: "required check outside the changed directories runs on an absolute paths match",
			files: fakeTreeReader{
				"web/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  build:
    run: npm run build
    paths: ["/go.mod"]
`),
			},
			changedPaths:   []string{"go.mod"},
			includedPaths:  []string{"/"},
			requiredChecks: []string{"/web/build"},
			assert: func(t *testing.T, plan *Plan) {
				if got := runnableNames(plan); !reflect.DeepEqual(got, []string{"web/build"}) {
					t.Fatalf("runnable names = %#v, want [web/build]", got)
				}
			},
		},
		{
			name: "required check in an undiscovered file that does not parse",
			files: fakeTreeReader{
				"web/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  build:
    run: npm run build
    timeout: nope
`),
			},
			changedPaths:   []string{"README.md"},
			includedPaths:  []string{"/"},
			requiredChecks: []string{"web/build"},
			assert: func(t *testing.T, plan *Plan) {
				if got := erroredNames(plan); !reflect.DeepEqual(got, []string{"web/build"}) {
					t.Fatalf("errored names = %#v, want [web/build]", got)
				}
				if !strings.Contains(plan.Errored[0].Reason, "timeout") {
					t.Fatalf("errored reason = %q, want timeout", plan.Errored[0].Reason)
				}
			},
		},
		{
			name: "parse error isolates one file",
			files: fakeTreeReader{
				"/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  root:
    run: root-test
`),
				"backend/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  test:
    run: backend-test
    timeout: nope
`),
			},
			changedPaths:  []string{"backend/app.go"},
			includedPaths: []string{"/"},
			assert: func(t *testing.T, plan *Plan) {
				if got := runnableNames(plan); !reflect.DeepEqual(got, []string{"root"}) {
					t.Fatalf("runnable names = %#v, want [root]", got)
				}
				if got := erroredNames(plan); !reflect.DeepEqual(got, []string{"backend/test"}) {
					t.Fatalf("errored names = %#v, want [backend/test]", got)
				}
				if !strings.Contains(plan.Errored[0].Reason, "timeout") {
					t.Fatalf("errored reason = %q, want timeout", plan.Errored[0].Reason)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := ResolvePlan(context.Background(), tt.files, "root", tt.changedPaths, tt.includedPaths, tt.requiredChecks)
			if err != nil {
				t.Fatalf("ResolvePlan() error = %v", err)
			}
			tt.assert(t, plan)
		})
	}
}

func TestResolvePlanReturnsReadError(t *testing.T) {
	errBoom := errors.New("boom")
	reader := readErrorTree{err: errBoom}
	_, err := ResolvePlan(context.Background(), reader, "root", []string{"backend/app.go"}, []string{"/"}, nil)
	if !errors.Is(err, errBoom) {
		t.Fatalf("ResolvePlan() error = %v, want wrapping boom", err)
	}
}

type readErrorTree struct {
	err error
}

func (r readErrorTree) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, r.err
}

type countingTreeReader struct {
	files fakeTreeReader
	reads map[string]int
}

func (r *countingTreeReader) ReadFile(ctx context.Context, rootTreeID, p string) ([]byte, error) {
	if r.reads == nil {
		r.reads = map[string]int{}
	}
	r.reads[rootTreeID+":"+p]++
	return r.files.ReadFile(ctx, rootTreeID, p)
}

func TestResolveWithResolverMemoizesCheckFilesAcrossPlanLookups(t *testing.T) {
	reader := &countingTreeReader{
		files: fakeTreeReader{
			"/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  root:
    run: root-test
`),
			"backend/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  unit:
    run: go test ./...
`),
		},
	}
	resolver := NewResolver(reader)
	for i := 0; i < 5; i++ {
		plan, err := ResolveWithResolver(
			context.Background(),
			resolver,
			"tree_1",
			[]string{"backend/app.go"},
			[]string{"/"},
			[]string{"root", "backend/unit"},
		)
		if err != nil {
			t.Fatalf("ResolveWithResolver #%d: %v", i, err)
		}
		if got := runnableNames(plan); !reflect.DeepEqual(got, []string{"root", "backend/unit"}) {
			t.Fatalf("runnable names #%d = %#v, want [root backend/unit]", i, got)
		}
	}
	if got := reader.reads["tree_1:/.gitslice/checks.yaml"]; got != 1 {
		t.Fatalf("reads of /.gitslice/checks.yaml = %d, want 1", got)
	}
	if got := reader.reads["tree_1:backend/.gitslice/checks.yaml"]; got != 1 {
		t.Fatalf("reads of backend/.gitslice/checks.yaml = %d, want 1", got)
	}
}

func runnableNames(plan *Plan) []string {
	out := make([]string, 0, len(plan.Runnable))
	for _, check := range plan.Runnable {
		out = append(out, check.Name)
	}
	return out
}

func TestRequiredForPathsAndListForFolder(t *testing.T) {
	files := fakeTreeReader{
		"/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  lint:
    run: lint
    required: true
  optional_root:
    run: echo hi
`),
		"web/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  build:
    run: npm run build
    required: true
    paths: ["src/**"]
  docs:
    run: npm run docs
    required: false
`),
		"broken/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  bad:
    run: echo x
    timeout: nope
`),
	}
	resolver := NewResolver(files)

	// Touching web/src/app.tsx triggers both root lint and web/build.
	got, err := RequiredForPaths(context.Background(), resolver, "tree_1", []string{"web/src/app.tsx"})
	if err != nil {
		t.Fatalf("RequiredForPaths(web/src/app.tsx): %v", err)
	}
	if want := []string{"lint", "web/build"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RequiredForPaths(web/src/app.tsx) = %#v, want %#v", got, want)
	}

	// Touching web/README.md does not match web/build's paths: ["src/**"], so only root lint is required.
	got, err = RequiredForPaths(context.Background(), resolver, "tree_1", []string{"web/README.md"})
	if err != nil {
		t.Fatalf("RequiredForPaths(web/README.md): %v", err)
	}
	if want := []string{"lint"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RequiredForPaths(web/README.md) = %#v, want %#v", got, want)
	}

	// Plan sets CheckSpec.Required accurately.
	plan, err := ResolveWithResolver(context.Background(), resolver, "tree_1", []string{"web/src/app.tsx"}, []string{"/"}, nil)
	if err != nil {
		t.Fatalf("ResolveWithResolver: %v", err)
	}
	if spec, ok := plan.Spec("web/build"); !ok || !spec.Required {
		t.Fatalf("plan.Spec(web/build) = %+v, %v, want Required=true", spec, ok)
	}
	if spec, ok := plan.Spec("optional_root"); !ok || spec.Required {
		t.Fatalf("plan.Spec(optional_root) = %+v, %v, want Required=false", spec, ok)
	}

	// ListForFolder lists ancestor checks from root down to web.
	listed, problems, err := ListForFolder(context.Background(), resolver, "tree_1", "web/src")
	if err != nil {
		t.Fatalf("ListForFolder(web/src): %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("ListForFolder(web/src) unexpected problems: %+v", problems)
	}
	var ids []string
	for _, c := range listed {
		ids = append(ids, c.ID)
	}
	if want := []string{"lint", "optional_root", "web/build", "web/docs"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ListForFolder(web/src) ids = %#v, want %#v", ids, want)
	}

	// ListForFolder reports parse problems without dropping valid ancestors.
	listedBroken, problemsBroken, err := ListForFolder(context.Background(), resolver, "tree_1", "broken")
	if err != nil {
		t.Fatalf("ListForFolder(broken): %v", err)
	}
	if len(listedBroken) != 2 {
		t.Fatalf("ListForFolder(broken) checks = %d, want 2 ancestor checks", len(listedBroken))
	}
	if len(problemsBroken) != 1 || problemsBroken[0].SourceDir != "broken" {
		t.Fatalf("ListForFolder(broken) problems = %+v, want 1 problem in broken", problemsBroken)
	}
}

func skippedNames(plan *Plan) []string {
	out := make([]string, 0, len(plan.Skipped))
	for _, check := range plan.Skipped {
		out = append(out, check.Name)
	}
	return out
}

func erroredNames(plan *Plan) []string {
	out := make([]string, 0, len(plan.Errored))
	for _, check := range plan.Errored {
		out = append(out, check.Name)
	}
	return out
}

func requireRunnableCount(t *testing.T, plan *Plan, want int) {
	t.Helper()
	if len(plan.Runnable) != want {
		t.Fatalf("len(Runnable) = %d, want %d; plan = %#v", len(plan.Runnable), want, plan)
	}
}
