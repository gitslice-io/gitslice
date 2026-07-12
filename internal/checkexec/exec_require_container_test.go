package checkexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/checks"
)

func TestRunRequiresContainerWhenConfigured(t *testing.T) {
	t.Setenv("GITSLICE_CHECKS_REQUIRE_CONTAINER", "true")
	root := t.TempDir()
	marker := filepath.Join(root, "executed")

	result, err := Run(context.Background(), root, checks.CheckSpec{
		Name: "security/check",
		Run:  "touch executed",
	})
	if err == nil {
		t.Fatal("Run() error = nil, want host-mode rejection")
	}
	want := `check "security/check" must declare an image or setup: host-mode execution is disabled on this daemon`
	if err.Error() != want {
		t.Fatalf("Run() error = %q, want %q", err, want)
	}
	if result != (Result{}) {
		t.Fatalf("result = %#v, want empty Result", result)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want not exist", statErr)
	}
}

func TestRunAllowsHostModeByDefault(t *testing.T) {
	t.Setenv("GITSLICE_CHECKS_REQUIRE_CONTAINER", "")

	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{Run: "printf ok"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "passed" || result.ExitCode != 0 || result.Log != "ok" {
		t.Fatalf("result = %#v, want passed exit 0 with log ok", result)
	}
}

func TestRunWithImageUsesContainerWhenRequired(t *testing.T) {
	t.Setenv("GITSLICE_CHECKS_REQUIRE_CONTAINER", "1")
	t.Setenv("PATH", t.TempDir())

	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{
		Name:  "security/check",
		Image: "example.invalid/check:latest",
		Run:   "true",
	})
	if err != nil {
		if strings.Contains(err.Error(), "must declare an image or setup") {
			t.Fatalf("Run() took host-mode rejection path: %v", err)
		}
		t.Fatalf("Run() unexpected error = %v", err)
	}
	if result.Status != "errored" || !strings.Contains(result.Summary, "container runtime not found") {
		t.Fatalf("result = %#v, want missing container runtime result", result)
	}
}
