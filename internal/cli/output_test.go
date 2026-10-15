package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONFieldSelection(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	serverAddr := startFakeAuthStatusServer(t, "secret-token", "user_alice")
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "secret-token",
		SubjectID:  "stale_local_subject",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), []string{"auth", "status", "--json=signed_in,server_addr"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("field-selected JSON is invalid: %v\n%s", err, stdout.String())
	}
	if len(got) != 2 || got["signed_in"] != true || got["server_addr"] != serverAddr {
		t.Fatalf("unexpected selected fields: %#v", got)
	}
	if _, ok := got["subject_id"]; ok {
		t.Fatalf("unexpected unselected subject_id field: %#v", got)
	}
}

func TestJSONFieldSelectionRejectsUnknownField(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"auth", "status", "--json=missing"})
	if err == nil {
		t.Fatal("expected unknown field error")
	}
	if !isUserErrorCode(err, "unknown_json_field") {
		t.Fatalf("expected unknown_json_field, got %T: %v", err, err)
	}
}

func TestTemplateOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"auth", "status", "--template", "{{.signed_in}} {{.reason}}"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "false not_logged_in"; got != want {
		t.Fatalf("template output = %q, want %q", got, want)
	}
}

func TestTemplateOutputUsesSelectedFields(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"auth", "status", "--json=reason", "--template", "{{.reason}}"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "not_logged_in"; got != want {
		t.Fatalf("template output = %q, want %q", got, want)
	}
}

func TestTemplateOutputRejectsMissingField(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"auth", "status", "--template", "{{.missing}}"})
	if err == nil {
		t.Fatal("expected missing template field error")
	}
	if !isUserErrorCode(err, "template_failed") {
		t.Fatalf("expected template_failed, got %T: %v", err, err)
	}
}

func TestJQOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"auth", "status", "--jq", ".reason"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "not_logged_in"; got != want {
		t.Fatalf("jq output = %q, want %q", got, want)
	}
}

func TestJQOutputUsesSelectedFields(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"auth", "status", "--json=reason", "--jq", "{reason: .reason}"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("jq object output is invalid JSON: %v\n%s", err, stdout.String())
	}
	if got["reason"] != "not_logged_in" {
		t.Fatalf("unexpected jq output: %#v", got)
	}
}

func TestJQOutputRejectsInvalidExpression(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"auth", "status", "--jq", "["})
	if err == nil {
		t.Fatal("expected invalid jq error")
	}
	if !isUserErrorCode(err, "invalid_jq") {
		t.Fatalf("expected invalid_jq, got %T: %v", err, err)
	}
}

func TestJQAndTemplateAreMutuallyExclusive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"auth", "status", "--jq", ".reason", "--template", "{{.reason}}"})
	if err == nil {
		t.Fatal("expected invalid format error")
	}
	if !isUserErrorCode(err, "invalid_format") {
		t.Fatalf("expected invalid_format, got %T: %v", err, err)
	}
}
