//go:build unix

package checkexec

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/checks"
)

func shortOutputWaitDelay(t *testing.T) {
	t.Helper()
	prev := outputWaitDelay
	outputWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { outputWaitDelay = prev })
}

// A check that leaves a process holding its output open still finishes.
func TestRunHostLeftoverProcessDoesNotHoldTheCheck(t *testing.T) {
	shortOutputWaitDelay(t)
	start := time.Now()
	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{
		Run:     "sleep 30 & printf ok",
		Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Run() took %s, want it bounded by the output wait", elapsed)
	}
	if result.Status != "passed" || result.ExitCode != 0 {
		t.Fatalf("result = %#v, want passed exit 0", result)
	}
	if !strings.HasPrefix(result.Log, "ok") || !strings.Contains(result.Log, "kept its output open") {
		t.Fatalf("log = %q, want the output and a note about the stopped processes", result.Log)
	}
}

// A timed-out check finishes even when a process it started left its
// process group (and so survives the group kill) with the output open.
func TestRunHostTimeoutWithEscapedProcessFinishes(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	shortOutputWaitDelay(t)
	start := time.Now()
	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{
		Run:     "setsid sleep 5 & sleep 30",
		Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("Run() took %s, want it bounded by the output wait", elapsed)
	}
	if result.Status != "errored" || !strings.Contains(result.Summary, "timed out") {
		t.Fatalf("result = %#v, want errored timed out", result)
	}
}
