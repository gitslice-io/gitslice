package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestVersionCommandEmitsBuildInfo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"version", "--json=version,go_version,dirty"}); err != nil {
		t.Fatalf("version failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("version output is not JSON: %v\n%s", err, stdout.String())
	}
	if got["version"] == "" || got["go_version"] == "" {
		t.Fatalf("version output missing fields: %#v", got)
	}
	if _, ok := got["dirty"]; !ok {
		t.Fatalf("version output missing dirty field: %#v", got)
	}
	if _, ok := got["commit"]; ok {
		t.Fatalf("unexpected unselected commit field: %#v", got)
	}
}
