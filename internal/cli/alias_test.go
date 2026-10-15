package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAliasCommandsAndExpansion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}

	if err := r.Run(context.Background(), []string{"alias", "list", "--json"}); err != nil {
		t.Fatalf("alias list failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var listed struct {
		Aliases []aliasEntryOutput `json:"aliases"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
		t.Fatalf("alias list output is not JSON: %v\n%s", err, stdout.String())
	}
	if len(listed.Aliases) != 0 {
		t.Fatalf("expected no aliases, got %#v", listed.Aliases)
	}

	stdout.Reset()
	if err := r.Run(context.Background(), []string{"alias", "set", "who", "version"}); err != nil {
		t.Fatalf("alias set failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "set alias who") {
		t.Fatalf("alias set output missing confirmation:\n%s", stdout.String())
	}

	stdout.Reset()
	if err := r.Run(context.Background(), []string{"who", "--json=version"}); err != nil {
		t.Fatalf("alias expansion failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var version map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &version); err != nil {
		t.Fatalf("alias expansion output is not JSON: %v\n%s", err, stdout.String())
	}
	if len(version) != 1 || version["version"] == "" {
		t.Fatalf("unexpected alias expansion output: %#v", version)
	}

	stdout.Reset()
	if err := r.Run(context.Background(), []string{"--json=version", "who"}); err != nil {
		t.Fatalf("alias expansion after global flag failed: %v\nstderr:\n%s", err, stderr.String())
	}
	version = map[string]any{}
	if err := json.Unmarshal(stdout.Bytes(), &version); err != nil {
		t.Fatalf("global-flag alias output is not JSON: %v\n%s", err, stdout.String())
	}
	if version["version"] == "" {
		t.Fatalf("unexpected global-flag alias output: %#v", version)
	}

	stdout.Reset()
	if err := r.Run(context.Background(), []string{"alias", "list"}); err != nil {
		t.Fatalf("alias list failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "who: version") {
		t.Fatalf("alias list missing alias:\n%s", stdout.String())
	}

	stdout.Reset()
	if err := r.Run(context.Background(), []string{"alias", "delete", "who", "--json"}); err != nil {
		t.Fatalf("alias delete failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var deleted map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &deleted); err != nil {
		t.Fatalf("alias delete output is not JSON: %v\n%s", err, stdout.String())
	}
	if deleted["name"] != "who" || deleted["deleted"] != true {
		t.Fatalf("unexpected alias delete output: %#v", deleted)
	}
}

func TestAliasRejectsReservedCommandName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"alias", "set", "status", "version"})
	if err == nil {
		t.Fatal("expected reserved alias to fail")
	}
	if !isUserErrorCode(err, "reserved_alias") {
		t.Fatalf("expected reserved_alias, got %T: %v", err, err)
	}
}
