package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRootHelpIncludesWorkflowExamples(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"help"}); err != nil {
		t.Fatalf("help failed: %v\nstderr:\n%s", err, stderr.String())
	}
	for _, want := range []string{
		"gs auth signup --username nico",
		"gs fs upload ./notes /nico/notes --recursive",
		"gs create --message \"update notes\"",
		"gs submit",
		"HELP TOPICS",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("root help missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "gs cs submit") {
		t.Fatalf("root help still advertises legacy gs cs submit:\n%s", stdout.String())
	}
}

func TestHelpTopics(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"environment", "GITSLICE_GRPC_ADDR"},
		{"formatting", "--template"},
		{"exit-codes", "4"},
		{"paths", "account-rooted"},
		{"slices", "nic/home"},
		{"webhooks", "X-Gitslice-Signature-256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
			if err := r.Run(context.Background(), []string{"help", tc.name}); err != nil {
				t.Fatalf("help %s failed: %v\nstderr:\n%s", tc.name, err, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("help %s missing %q:\n%s", tc.name, tc.want, stdout.String())
			}
		})
	}
}

func TestHelpCommandStillShowsCommandHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"help", "auth", "status"}); err != nil {
		t.Fatalf("help auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Show current authentication status") {
		t.Fatalf("expected command help, got:\n%s", stdout.String())
	}
}

func TestFSListHelpClarifiesRemoteDefault(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"help", "fs", "ls"}); err != nil {
		t.Fatalf("help fs ls failed: %v\nstderr:\n%s", err, stderr.String())
	}
	for _, want := range []string{
		"gs fs ls [remote-path]",
		"signed-in home slice root",
		"not the local ~/ directory",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help fs ls missing %q:\n%s", want, stdout.String())
		}
	}
}
