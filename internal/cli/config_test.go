package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigCommandsListGetSetAndRedactToken(t *testing.T) {
	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	r := Runner{Home: home, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: "127.0.0.1:50051",
		Token:      "secret-token",
		SubjectID:  "user_alice",
		Aliases:    map[string]string{"who": "version"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"cfg", "list", "--json"}); err != nil {
		t.Fatalf("config list failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "secret-token") {
		t.Fatalf("config list leaked token:\n%s", stdout.String())
	}
	var listed configOutput
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
		t.Fatalf("config list output is not JSON: %v\n%s", err, stdout.String())
	}
	if listed.ServerAddr != "127.0.0.1:50051" || listed.SubjectID != "user_alice" || !listed.TokenPresent {
		t.Fatalf("unexpected config list output: %#v", listed)
	}

	stdout.Reset()
	if err := r.Run(context.Background(), []string{"cfg", "get", "server_addr"}); err != nil {
		t.Fatalf("config get failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "127.0.0.1:50051" {
		t.Fatalf("unexpected config get output: %q", stdout.String())
	}

	stdout.Reset()
	if err := r.Run(context.Background(), []string{"cfg", "set", "server_addr", "127.0.0.1:60000"}); err != nil {
		t.Fatalf("config set failed: %v\nstderr:\n%s", err, stderr.String())
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerAddr != "127.0.0.1:60000" || cfg.Token != "secret-token" || cfg.SubjectID != "user_alice" || cfg.Aliases["who"] != "version" {
		t.Fatalf("config set did not preserve auth fields: %#v", cfg)
	}
}

func TestConfigRejectsTokenRead(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{Token: "secret-token"}); err != nil {
		t.Fatal(err)
	}
	err := r.Run(context.Background(), []string{"config", "get", "token"})
	if err == nil {
		t.Fatal("expected token read to fail")
	}
	if !isUserErrorCode(err, "secret_config_key") {
		t.Fatalf("expected secret_config_key, got %T: %v", err, err)
	}
}
