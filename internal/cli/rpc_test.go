package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestRPCListIncludesGeneratedMethods(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"rpc", "list", "--json"}); err != nil {
		t.Fatalf("rpc list failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var got struct {
		Methods []rpcMethodOutput `json:"methods"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("rpc list output is not JSON: %v\n%s", err, stdout.String())
	}
	for _, method := range got.Methods {
		if method.FullMethod == "/gitslice.core.v1.AuthService/GetAuthStatus" {
			return
		}
	}
	t.Fatalf("rpc list missing AuthService/GetAuthStatus: %#v", got.Methods)
}

func TestRPCCallAuthStatus(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	serverAddr := startFakeAuthStatusServer(t, "secret-token", "user_alice")
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "secret-token",
		SubjectID:  "user_alice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), []string{"rpc", "call", "AuthService/GetAuthStatus", "--request", "{}"}); err != nil {
		t.Fatalf("rpc call failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("rpc call output is not JSON: %v\n%s", err, stdout.String())
	}
	if got["subject_id"] != "user_alice" {
		t.Fatalf("unexpected rpc response: %#v", got)
	}

	stdout.Reset()
	if err := r.Run(context.Background(), []string{"rpc", "call", "/gitslice.core.v1.AuthService/GetAuthStatus", "--request", "{}", "--json=subject_id"}); err != nil {
		t.Fatalf("field-selected rpc call failed: %v\nstderr:\n%s", err, stderr.String())
	}
	got = map[string]any{}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("field-selected rpc output is not JSON: %v\n%s", err, stdout.String())
	}
	if len(got) != 1 || got["subject_id"] != "user_alice" {
		t.Fatalf("unexpected field-selected rpc response: %#v", got)
	}
}
