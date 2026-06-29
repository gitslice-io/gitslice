package checks

import (
	"strings"
	"testing"
	"time"
)

func TestParseMergesDefaultsAndOverrides(t *testing.T) {
	file, err := Parse([]byte(`
version: 1
defaults:
  image: "golang:1.22"
  timeout: "10m"
  env:
    CGO_ENABLED: "0"
    SHARED: "default"
  network: true
checks:
  test:
    description: "unit tests"
    run: "go test ./..."
    image: "golang:1.23"
    timeout: "30s"
    paths: ["**/*.go"]
    include: ["/go.mod", "/go.sum"]
    working_dir: "cmd/service"
    env:
      SHARED: "check"
      EXTRA: "1"
    network: false
  lint:
    run: "go vet ./..."
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if file.Version != 1 {
		t.Fatalf("Version = %d, want 1", file.Version)
	}
	if file.Defaults.Timeout != 10*time.Minute {
		t.Fatalf("Defaults.Timeout = %v, want 10m", file.Defaults.Timeout)
	}

	test := file.Checks["test"]
	if test.Description != "unit tests" || test.Run != "go test ./..." {
		t.Fatalf("test check = %#v", test)
	}
	if test.Image != "golang:1.23" {
		t.Fatalf("test.Image = %q", test.Image)
	}
	if test.Timeout != 30*time.Second {
		t.Fatalf("test.Timeout = %v, want 30s", test.Timeout)
	}
	if test.Network {
		t.Fatalf("test.Network = true, want false override")
	}
	if test.WorkingDir != "cmd/service" {
		t.Fatalf("test.WorkingDir = %q", test.WorkingDir)
	}
	if got := test.Env["CGO_ENABLED"]; got != "0" {
		t.Fatalf("test.Env[CGO_ENABLED] = %q", got)
	}
	if got := test.Env["SHARED"]; got != "check" {
		t.Fatalf("test.Env[SHARED] = %q", got)
	}
	if got := test.Env["EXTRA"]; got != "1" {
		t.Fatalf("test.Env[EXTRA] = %q", got)
	}

	lint := file.Checks["lint"]
	if lint.Image != "golang:1.22" {
		t.Fatalf("lint.Image = %q", lint.Image)
	}
	if lint.Timeout != 10*time.Minute {
		t.Fatalf("lint.Timeout = %v, want default 10m", lint.Timeout)
	}
	if !lint.Network {
		t.Fatalf("lint.Network = false, want default true")
	}
	if lint.WorkingDir != "." {
		t.Fatalf("lint.WorkingDir = %q, want .", lint.WorkingDir)
	}
}

func TestParseRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "missing version",
			yaml: `
checks:
  test:
    run: go test ./...
`,
			wantErr: "version is required",
		},
		{
			name: "unsupported version",
			yaml: `
version: 2
checks:
  test:
    run: go test ./...
`,
			wantErr: "unsupported version 2",
		},
		{
			name: "invalid local name",
			yaml: `
version: 1
checks:
  bad/name:
    run: go test ./...
`,
			wantErr: "invalid local name",
		},
		{
			name: "missing run",
			yaml: `
version: 1
checks:
  test:
    image: golang:1.22
`,
			wantErr: "run is required",
		},
		{
			name: "bad default timeout",
			yaml: `
version: 1
defaults:
  timeout: not-a-duration
checks:
  test:
    run: go test ./...
`,
			wantErr: "defaults.timeout",
		},
		{
			name: "bad check timeout",
			yaml: `
version: 1
checks:
  test:
    run: go test ./...
    timeout: nope
`,
			wantErr: `check "test" timeout`,
		},
		{
			name: "paths ascent",
			yaml: `
version: 1
checks:
  test:
    run: go test ./...
    paths: ["../*.go"]
`,
			wantErr: "contains .. segment",
		},
		{
			name: "include ascent",
			yaml: `
version: 1
checks:
  test:
    run: go test ./...
    include: ["../go.mod"]
`,
			wantErr: "contains .. segment",
		},
		{
			name: "working dir ascent",
			yaml: `
version: 1
checks:
  test:
    run: go test ./...
    working_dir: "../backend"
`,
			wantErr: "contains .. segment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatal("Parse() error = nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
