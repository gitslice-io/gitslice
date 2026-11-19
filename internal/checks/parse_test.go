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
  setup:
    - "go env -w GOPROXY=off"
  cache:
    - "/go/pkg/mod"
    - "/root/.cache/go-build"
  timeout: "10m"
  env:
    CGO_ENABLED: "0"
    SHARED: "default"
  network: true
  memory: "6g"
  cpus: "3"
checks:
  test:
    description: "unit tests"
    run: "go test ./..."
    image: "golang:1.23"
    setup:
      - "apt-get update"
      - "apt-get install -y make"
    cache:
      - "/tmp/check-cache"
    timeout: "30s"
    paths: ["**/*.go"]
    include: ["/go.mod", "/go.sum"]
    working_dir: "cmd/service"
    env:
      SHARED: "check"
      EXTRA: "1"
    network: false
    memory: "8g"
    cpus: "4"
  lint:
    run: "go vet ./..."
  empty_setup:
    run: "go test ./..."
    setup: []
  empty_cache:
    run: "go test ./..."
    cache: []
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
	wantTestSetup := []string{"apt-get update", "apt-get install -y make"}
	if got := test.Setup; !stringSlicesEqual(got, wantTestSetup) {
		t.Fatalf("test.Setup = %#v, want %#v", got, wantTestSetup)
	}
	wantTestCache := []string{"/tmp/check-cache"}
	if got := test.Cache; !stringSlicesEqual(got, wantTestCache) {
		t.Fatalf("test.Cache = %#v, want %#v", got, wantTestCache)
	}
	if test.Timeout != 30*time.Second {
		t.Fatalf("test.Timeout = %v, want 30s", test.Timeout)
	}
	if test.Network {
		t.Fatalf("test.Network = true, want false override")
	}
	if test.Memory != "8g" {
		t.Fatalf("test.Memory = %q, want 8g", test.Memory)
	}
	if test.CPUs != "4" {
		t.Fatalf("test.CPUs = %q, want 4", test.CPUs)
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
	wantDefaultSetup := []string{"go env -w GOPROXY=off"}
	if got := lint.Setup; !stringSlicesEqual(got, wantDefaultSetup) {
		t.Fatalf("lint.Setup = %#v, want %#v", got, wantDefaultSetup)
	}
	wantDefaultCache := []string{"/go/pkg/mod", "/root/.cache/go-build"}
	if got := lint.Cache; !stringSlicesEqual(got, wantDefaultCache) {
		t.Fatalf("lint.Cache = %#v, want %#v", got, wantDefaultCache)
	}
	if lint.Timeout != 10*time.Minute {
		t.Fatalf("lint.Timeout = %v, want default 10m", lint.Timeout)
	}
	if !lint.Network {
		t.Fatalf("lint.Network = false, want default true")
	}
	if lint.Memory != "6g" {
		t.Fatalf("lint.Memory = %q, want default 6g", lint.Memory)
	}
	if lint.CPUs != "3" {
		t.Fatalf("lint.CPUs = %q, want default 3", lint.CPUs)
	}
	if lint.WorkingDir != "." {
		t.Fatalf("lint.WorkingDir = %q, want .", lint.WorkingDir)
	}

	emptySetup := file.Checks["empty_setup"]
	if len(emptySetup.Setup) != 0 {
		t.Fatalf("empty_setup.Setup = %#v, want empty override", emptySetup.Setup)
	}

	emptyCache := file.Checks["empty_cache"]
	if len(emptyCache.Cache) != 0 {
		t.Fatalf("empty_cache.Cache = %#v, want empty override", emptyCache.Cache)
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
			name: "default cache relative path",
			yaml: `
version: 1
defaults:
  cache: ["relative/cache"]
checks:
  test:
    run: go test ./...
`,
			wantErr: "defaults cache",
		},
		{
			name: "check cache relative path",
			yaml: `
version: 1
checks:
  test:
    run: go test ./...
    cache: ["relative/cache"]
`,
			wantErr: `check "test" cache`,
		},
		{
			name: "check cache ascent",
			yaml: `
version: 1
checks:
  test:
    run: go test ./...
    cache: ["/root/../cache"]
`,
			wantErr: "contains .. segment",
		},
		{
			name: "check cache root",
			yaml: `
version: 1
checks:
  test:
    run: go test ./...
    cache: ["/"]
`,
			wantErr: "must not be the container root",
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
		{
			name: "check image leading dash flag injection",
			yaml: `
version: 1
checks:
  test:
    image: "--privileged"
    run: id
`,
			wantErr: "must not start with '-'",
		},
		{
			name: "service image whitespace injection",
			yaml: `
version: 1
services:
  db:
    image: "postgres:16 --privileged"
checks:
  test:
    image: busybox
    services: [db]
    run: "true"
`,
			wantErr: "whitespace or control characters",
		},
		{
			name:    "default image newline injection",
			yaml:    "version: 1\ndefaults:\n  image: \"busybox\\nRUN id\"\nchecks:\n  test:\n    run: \"true\"\n",
			wantErr: "whitespace or control characters",
		},
		{
			name:    "setup command newline injection",
			yaml:    "version: 1\nchecks:\n  test:\n    image: busybox\n    setup: [\"echo ok\\nCOPY / /x\"]\n    run: \"true\"\n",
			wantErr: "must not contain CR, LF, or NUL",
		},
		{
			name: "invalid env variable key",
			yaml: `
version: 1
checks:
  test:
    image: busybox
    env:
      "BAD-KEY": "1"
    run: "true"
`,
			wantErr: "invalid env variable name",
		},
		{
			name:    "env variable value newline injection",
			yaml:    "version: 1\nchecks:\n  test:\n    image: busybox\n    env:\n      TOKEN: \"abc\\nLD_PRELOAD=/tmp/evil.so\"\n    run: \"true\"\n",
			wantErr: "must not contain CR, LF, or NUL",
		},
		{
			name: "invalid memory limit",
			yaml: `
version: 1
checks:
  test:
    image: busybox
    memory: "-4g"
    run: "true"
`,
			wantErr: "invalid memory limit",
		},
		{
			name: "invalid cpus limit",
			yaml: `
version: 1
checks:
  test:
    image: busybox
    cpus: "0"
    run: "true"
`,
			wantErr: "invalid cpus limit",
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

func TestValidateImageRefAndResourceLimits(t *testing.T) {
	validImages := []string{
		"a",
		"busybox",
		"postgres:16",
		"postgres:16-alpine",
		"golang:1.24.1",
		"golangci/golangci-lint:v1.59",
		"localhost:5000/org/app:v1.2.3",
		"example.invalid/check:latest",
		"ghcr.io/org/repo@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"gcr.io/org/repo:v1@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"gitslice-ci/0123456789abcdef01234567",
		"gitslice-ci/build-0123456789abcdef:0123456789abcdef01234567",
	}
	for _, img := range validImages {
		if err := ValidateImageRef(img); err != nil {
			t.Fatalf("ValidateImageRef(%q) unexpected error = %v", img, err)
		}
	}

	invalidImages := []string{
		"",
		"-v",
		"--privileged",
		" busybox",
		"busybox ",
		"busybox latest",
		"busybox\nRUN id",
		"busybox\rFROM scratch",
		"busybox\x00evil",
		"../busybox",
		"org/../busybox",
		"busybox:",
		"busybox:bad:tag",
		"busybox@md5:1234",
		"localhost:99999/app:v1",
	}
	for _, img := range invalidImages {
		if err := ValidateImageRef(img); err == nil {
			t.Fatalf("ValidateImageRef(%q) = nil, want error", img)
		}
	}

	if got, err := ParseMemoryBytes("8g"); err != nil || got != 8<<30 || FormatMemoryBytes(got) != "8g" {
		t.Fatalf("ParseMemoryBytes(8g) = %d, %v (fmt %q)", got, err, FormatMemoryBytes(got))
	}
	if got, err := ParseCPUs("2.5"); err != nil || got != 2.5 || FormatCPUs(got) != "2.5" {
		t.Fatalf("ParseCPUs(2.5) = %v, %v (fmt %q)", got, err, FormatCPUs(got))
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseRequiredField(t *testing.T) {
	file, err := Parse([]byte(`
version: 1
defaults:
  required: true
checks:
  inherited:
    run: go test ./...
  opted_out:
    run: go vet ./...
    required: false
  explicit_true:
    run: go build ./...
    required: true
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !file.Defaults.Required {
		t.Fatal("Defaults.Required = false, want true")
	}
	if !file.Checks["inherited"].Required {
		t.Fatal("inherited.Required = false, want true (from defaults)")
	}
	if file.Checks["opted_out"].Required {
		t.Fatal("opted_out.Required = true, want false (explicit override)")
	}
	if !file.Checks["explicit_true"].Required {
		t.Fatal("explicit_true.Required = false, want true")
	}
}
