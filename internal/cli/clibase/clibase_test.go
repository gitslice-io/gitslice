package clibase

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestOptionsAndDiffContext(t *testing.T) {
	if (Options{Format: "text"}).JSONOutput() {
		t.Fatal("text format should not be JSONOutput")
	}
	if !(Options{Format: "json"}).JSONOutput() || !(Options{JQ: ".id"}).JSONOutput() || !(Options{Template: "{{.id}}"}).JSONOutput() || !(Options{JSONFields: []string{"id"}}).JSONOutput() {
		t.Fatal("expected JSONOutput true for --json/--jq/--template/--json=fields")
	}
	if got := (DiffContext{}).Lines(); got != 3 {
		t.Fatalf("default Lines() = %d, want 3", got)
	}
	if got := (DiffContext{Context: 0, Set: true}).Lines(); got != 0 {
		t.Fatalf("-U0 Lines() = %d, want 0", got)
	}
}

func TestErrorsAndExitCodes(t *testing.T) {
	err := UserError("not_logged_in", "not signed in", "Run gs auth login.")
	if !IsUserErrorCode(err, "not_logged_in") {
		t.Fatalf("IsUserErrorCode failed for %v", err)
	}
	if got := ExitCodeForError(err); got != 4 {
		t.Fatalf("ExitCodeForError(not_logged_in) = %d, want 4", got)
	}
	if got := ExitCodeForError(context.Canceled); got != 2 {
		t.Fatalf("ExitCodeForError(Canceled) = %d, want 2", got)
	}
	if got := ExitCodeForError(grpcstatus.Error(codes.Unauthenticated, "bad token")); got != 4 {
		t.Fatalf("ExitCodeForError(Unauthenticated) = %d, want 4", got)
	}
	hinted := WithDomainErrorHints(errors.New("boom"), DomainErrorOverrides{
		codes.NotFound: {Hint: "try list"},
	})
	base, ov := ExtractDomainErrorOverrides(hinted)
	if base.Error() != "boom" || ov[codes.NotFound].Hint != "try list" {
		t.Fatalf("ExtractDomainErrorOverrides = (%v, %#v)", base, ov)
	}
}

func TestOutputJSONFieldSelectionAndJQ(t *testing.T) {
	type sample struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var buf bytes.Buffer
	if err := WriteJSONOutput(&buf, Options{JSONFields: []string{"id"}}, sample{ID: "123", Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"id": "123"`) || strings.Contains(buf.String(), "alpha") {
		t.Fatalf("unexpected field-filtered JSON:\n%s", buf.String())
	}
	buf.Reset()
	if err := WriteJSONOutput(&buf, Options{JQ: ".name"}, sample{ID: "123", Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "alpha" {
		t.Fatalf("jq output = %q, want alpha", buf.String())
	}
}

func TestSchemaAnnotations(t *testing.T) {
	type out struct {
		Slug string `json:"slug"`
	}
	cmd := &cobra.Command{Use: "demo", RunE: func(*cobra.Command, []string) error { return nil }}
	WithSchemaOutput[out](cmd)
	typ, ok := SchemaOutputTypeForCommand(cmd)
	if !ok || typ.Name() != "out" {
		t.Fatalf("SchemaOutputTypeForCommand = (%v, %v)", typ, ok)
	}
	if got := cmd.Annotations[SchemaOutputAnnotation]; got != "slug" {
		t.Fatalf("SchemaOutputAnnotation = %q, want slug", got)
	}
}

func TestProgressReporterNoopWithoutSink(t *testing.T) {
	if p := StartProgress(context.Background(), "uploading", "files", 5); p != nil {
		t.Fatal("expected nil progress without sink")
	}
	var buf bytes.Buffer
	p := StartProgressEvery(ProgressSink{W: &buf}, "syncing", "files", 2, time.Millisecond, 5*time.Millisecond)
	p.Add(2)
	time.Sleep(15 * time.Millisecond)
	p.Finish()
	if !strings.Contains(buf.String(), "syncing 2/2 files (100%)") {
		t.Fatalf("progress output = %q", buf.String())
	}
}
