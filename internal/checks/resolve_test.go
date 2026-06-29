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
  timeout: 10m
  env:
    CGO_ENABLED: "0"
    SHARED: default
  network: true
checks:
  test:
    run: go test ./...
    image: golang:1.23
    timeout: 30s
    working_dir: cmd/service
    env:
      SHARED: check
      EXTRA: "1"
    network: false
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
				if spec.Timeout != 30*time.Second {
					t.Fatalf("Timeout = %v, want 30s", spec.Timeout)
				}
				if spec.Network {
					t.Fatalf("Network = true, want false override")
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

func runnableNames(plan *Plan) []string {
	out := make([]string, 0, len(plan.Runnable))
	for _, check := range plan.Runnable {
		out = append(out, check.Name)
	}
	return out
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
