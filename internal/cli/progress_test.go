package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestProgressReportsLongRunsOnly(t *testing.T) {
	// A run that ends before the first report prints nothing.
	var quick bytes.Buffer
	p := startProgressEvery(progressSink{w: &quick}, "uploading", "files", 10, time.Hour, time.Hour)
	p.Add(10)
	p.Finish()
	if quick.Len() != 0 {
		t.Fatalf("a quick run printed %q", quick.String())
	}

	// A longer one prints a line per interval and a final count.
	var slow bytes.Buffer
	p = startProgressEvery(progressSink{w: &slow}, "uploading", "files", 1742, time.Millisecond, 5*time.Millisecond)
	p.Add(500)
	time.Sleep(30 * time.Millisecond)
	p.Add(1242)
	p.Finish()
	p.Finish()
	lines := strings.Split(strings.TrimSpace(slow.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("want progress lines and a final line, got %q", slow.String())
	}
	if !strings.Contains(slow.String(), "uploading 500/1742 files (28%)") {
		t.Fatalf("missing an intermediate count:\n%s", slow.String())
	}
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "uploading 1742/1742 files (100%), done in ") {
		t.Fatalf("last line = %q", last)
	}
	if strings.Contains(slow.String(), "\r") {
		t.Fatalf("a non-terminal should get plain lines:\n%q", slow.String())
	}

	// On a terminal the line rewrites itself; an unknown total is a count.
	var tty bytes.Buffer
	p = startProgressEvery(progressSink{w: &tty, tty: true}, "hydrating", "files", 0, time.Millisecond, time.Millisecond)
	p.Add(7)
	time.Sleep(20 * time.Millisecond)
	p.Finish()
	if !strings.Contains(tty.String(), "\r\x1b[Khydrating 7 files") || !strings.HasSuffix(tty.String(), "\n") {
		t.Fatalf("tty output = %q", tty.String())
	}
}

func TestProgressNeedsASink(t *testing.T) {
	if p := startProgress(context.Background(), "syncing", "paths", 3); p != nil {
		t.Fatal("a context without a sink should not report progress")
	}
	var p *progressReporter
	p.Add(1)
	p.Finish()
}

func TestProgressFollowsQuietAndJSON(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{args: []string{"config", "list"}, want: true},
		{args: []string{"config", "list", "--quiet"}, want: false},
		{args: []string{"config", "list", "--json"}, want: false},
	} {
		var stdout, stderr bytes.Buffer
		r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
		root := r.rootCommand()
		got := false
		root.SetArgs(tt.args)
		for _, cmd := range root.Commands() {
			if cmd.Name() != "config" {
				continue
			}
			for _, sub := range cmd.Commands() {
				if sub.Name() == "list" {
					sub.RunE = func(cmd *cobra.Command, args []string) error {
						p := startProgress(cmd.Context(), "x", "y", 1)
						got = p != nil
						p.Finish()
						return nil
					}
				}
			}
		}
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Fatalf("%v: progress = %v, want %v", tt.args, got, tt.want)
		}
	}
}
