package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gitslice-io/gitslice/internal/clientcache"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestSchemaCommandEmitsMachineReadableContract(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"schema"}); err != nil {
		t.Fatalf("schema failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var got struct {
		SchemaVersion string `json:"schema_version"`
		GlobalFlags   []struct {
			Name string `json:"name"`
		} `json:"global_flags"`
		Commands []struct {
			Use     string   `json:"use"`
			Aliases []string `json:"aliases"`
		} `json:"commands"`
		HelpTopics []struct {
			Name string `json:"name"`
		} `json:"help_topics"`
		ErrorOutput map[string]any `json:"error_output"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("schema output is not JSON: %v\n%s", err, stdout.String())
	}
	if got.SchemaVersion != "v1" {
		t.Fatalf("unexpected schema version %q", got.SchemaVersion)
	}
	if len(got.Commands) == 0 {
		t.Fatal("schema did not include commands")
	}
	globalFlags := map[string]bool{}
	for _, flag := range got.GlobalFlags {
		globalFlags[flag.Name] = true
	}
	for _, want := range []string{"--format", "--json", "--jq", "--template"} {
		if !globalFlags[want] {
			t.Fatalf("schema missing global flag %q", want)
		}
	}
	uses := map[string]bool{}
	aliases := map[string][]string{}
	for _, command := range got.Commands {
		uses[command.Use] = true
		aliases[command.Use] = command.Aliases
	}
	for _, want := range []string{"gs auth token", "gs auth logout", "gs alias list", "gs alias set <name> <command>", "gs browse [web-path]", "gs init <slice|account/slice>", "gs import <source>", "gs log [-- <path>]", "gs show <commit-id-or-prefix>", "gs version", "gs completion <shell>", "gs fs ls [remote-path]", "gs fs cat <absolute-path>", "gs fs mkdir <absolute-path>", "gs help <topic>"} {
		if !uses[want] {
			t.Fatalf("schema missing %q", want)
		}
	}
	for _, removed := range []string{"gs repo import github <owner/repo-or-url>", "gs repository import github <owner/repo-or-url>"} {
		if uses[removed] {
			t.Fatalf("schema still includes removed command %q", removed)
		}
	}
	for use, wantAlias := range map[string]string{
		"gs context":              "gs ctx",
		"gs config list":          "gs cfg list",
		"gs status":               "gs st",
		"gs slice list [account]": "gs slices list [account]",
	} {
		if !stringSliceContains(aliases[use], wantAlias) {
			t.Fatalf("schema aliases for %q missing %q: %#v", use, wantAlias, aliases[use])
		}
	}
	topics := map[string]bool{}
	for _, topic := range got.HelpTopics {
		topics[topic.Name] = true
	}
	for _, want := range []string{"environment", "formatting", "exit-codes", "paths", "slices"} {
		if !topics[want] {
			t.Fatalf("schema missing help topic %q", want)
		}
	}
	if got.ErrorOutput["stream"] != "stderr" {
		t.Fatalf("expected stderr error stream, got %#v", got.ErrorOutput["stream"])
	}
}

func TestLegacyRepoCommandIsRemoved(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"repo", "import", "github", "owner/repo"})
	if err == nil {
		t.Fatalf("legacy repo command unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), `unknown command "repo"`) && !strings.Contains(stderr.String(), `unknown command "repo"`) {
		t.Fatalf("legacy repo command error = %v\nstderr:\n%s", err, stderr.String())
	}
}

func TestSchemaCommandSupportsStructuredOutputFilters(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"schema", "--jq", `.global_flags[] | select(.name == "--jq") | .description`}); err != nil {
		t.Fatalf("schema --jq failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "filter structured output with a jq expression"; got != want {
		t.Fatalf("schema jq output = %q, want %q", got, want)
	}
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestRootHelpIncludesWorkflowExamples(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"help"}); err != nil {
		t.Fatalf("help failed: %v\nstderr:\n%s", err, stderr.String())
	}
	for _, want := range []string{
		"gs auth signup --username nic",
		"gs fs upload ./notes /nic/notes --recursive",
		"gs cs submit",
		"HELP TOPICS",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("root help missing %q:\n%s", want, stdout.String())
		}
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

func TestExitCodeForError(t *testing.T) {
	if got := exitCodeForError(nil); got != 0 {
		t.Fatalf("nil exit code = %d, want 0", got)
	}
	if got := exitCodeForError(errors.New("boom")); got != 1 {
		t.Fatalf("general exit code = %d, want 1", got)
	}
	if got := exitCodeForError(context.Canceled); got != 2 {
		t.Fatalf("canceled exit code = %d, want 2", got)
	}
	if got := exitCodeForError(userError("not_logged_in", "not logged in", "")); got != 4 {
		t.Fatalf("not logged in exit code = %d, want 4", got)
	}
	if got := exitCodeForError(status.Error(codes.Unauthenticated, "invalid token")); got != 4 {
		t.Fatalf("unauthenticated exit code = %d, want 4", got)
	}
}

func TestAuthStatusReportsSignedOut(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"auth", "status", "--json"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var got authStatusOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("auth status output is not JSON: %v\n%s", err, stdout.String())
	}
	if got.SignedIn {
		t.Fatalf("expected signed out status, got %#v", got)
	}
	if got.ServerAddr != "" || got.SubjectID != "" {
		t.Fatalf("signed out status exposed config fields: %#v", got)
	}
}

func TestAuthStatusReportsStoredLoginWithoutToken(t *testing.T) {
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
	if err := r.Run(context.Background(), []string{"auth", "status", "--json"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte("secret-token")) || bytes.Contains(stdout.Bytes(), []byte("token")) {
		t.Fatalf("auth status leaked token data:\n%s", stdout.String())
	}

	var got authStatusOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("auth status output is not JSON: %v\n%s", err, stdout.String())
	}
	if !got.SignedIn {
		t.Fatalf("expected signed in status, got %#v", got)
	}
	if got.ServerAddr != serverAddr || got.SubjectID != "user_alice" {
		t.Fatalf("unexpected auth status: %#v", got)
	}
}

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

func TestAuthStatusReportsInvalidStoredToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	serverAddr := startFakeAuthStatusServer(t, "valid-token", "user_alice")
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "stale-token",
		SubjectID:  "user_alice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), []string{"auth", "status", "--json"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var got authStatusOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("auth status output is not JSON: %v\n%s", err, stdout.String())
	}
	if got.SignedIn {
		t.Fatalf("expected signed out status, got %#v", got)
	}
	if got.ServerAddr != serverAddr || got.SubjectID != "" || got.Reason != "invalid_token" {
		t.Fatalf("unexpected auth status for invalid token: %#v", got)
	}
}

func TestAuthTokenPrintsValidatedToken(t *testing.T) {
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

	if err := r.Run(context.Background(), []string{"auth", "token"}); err != nil {
		t.Fatalf("auth token failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "secret-token"; got != want {
		t.Fatalf("auth token output = %q, want %q", got, want)
	}

	stdout.Reset()
	stderr.Reset()
	if err := r.Run(context.Background(), []string{"auth", "token", "--json"}); err != nil {
		t.Fatalf("auth token --json failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var got authTokenOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("auth token JSON is invalid: %v\n%s", err, stdout.String())
	}
	if got.Token != "secret-token" || got.ServerAddr != serverAddr || got.SubjectID != "user_alice" {
		t.Fatalf("unexpected auth token JSON: %#v", got)
	}
}

func TestAuthTokenRejectsInvalidStoredToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	serverAddr := startFakeAuthStatusServer(t, "valid-token", "user_alice")
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "stale-token",
		SubjectID:  "user_alice",
	}); err != nil {
		t.Fatal(err)
	}

	err := r.Run(context.Background(), []string{"auth", "token"})
	if err == nil {
		t.Fatal("expected invalid token error")
	}
	if !isUserErrorCode(err, "invalid_token") {
		t.Fatalf("expected invalid_token, got %T: %v", err, err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("auth token printed invalid token:\n%s", stdout.String())
	}
	if !strings.Contains(err.Error(), "gs auth status") || !strings.Contains(err.Error(), "gs auth signup --username alice") {
		t.Fatalf("expected recovery hint, got:\n%v", err)
	}
}

func TestAuthLogoutClearsTokenAndPreservesLocalConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: "127.0.0.1:50051",
		Token:      "secret-token",
		SubjectID:  "user_alice",
		Aliases:    map[string]string{"who": "version"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"auth", "logout", "--json"}); err != nil {
		t.Fatalf("auth logout failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("auth logout output is not JSON: %v\n%s", err, stdout.String())
	}
	if out["signed_in"] != false || out["was_signed_in"] != true || out["server_addr"] != "127.0.0.1:50051" {
		t.Fatalf("unexpected auth logout output: %#v", out)
	}

	cfg, err := r.readPartialUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "" || cfg.SubjectID != "" {
		t.Fatalf("auth logout did not clear credentials: %#v", cfg)
	}
	if cfg.ServerAddr != "127.0.0.1:50051" || cfg.Aliases["who"] != "version" {
		t.Fatalf("auth logout did not preserve local config: %#v", cfg)
	}
}

func TestUnauthenticatedErrorsIncludeRecoveryHint(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: "127.0.0.1:50051",
		Token:      "stale-token",
		SubjectID:  "user_alice",
	}); err != nil {
		t.Fatal(err)
	}

	err := r.enhanceCommandError(status.Error(codes.Unauthenticated, "invalid token"))
	if !strings.Contains(err.Error(), "gs auth status") {
		t.Fatalf("expected auth status recovery hint, got:\n%v", err)
	}
	if !strings.Contains(err.Error(), "gs auth signup --username alice") {
		t.Fatalf("expected username-specific signup hint, got:\n%v", err)
	}
}

func TestContextReportsAuthAndWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "src", "pkg")
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	serverAddr := startFakeAuthStatusServer(t, "secret-token", "user_alice")
	var stdout, stderr bytes.Buffer
	r := Runner{Home: home, Dir: nested, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "secret-token",
		SubjectID:  "stale_local_subject",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_config",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		CurrentChangesetID: "cs_123",
		CurrentPatchsetID:  "ps_123",
		BaseCommitID:       "cmt_state",
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"ctx", "--json"}); err != nil {
		t.Fatalf("context failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var got contextOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("context output is not JSON: %v\n%s", err, stdout.String())
	}
	if !got.SignedIn || got.SubjectID != "user_alice" || got.ServerAddr != serverAddr {
		t.Fatalf("unexpected auth context: %#v", got)
	}
	if got.Workspace == nil {
		t.Fatalf("expected workspace context: %#v", got)
	}
	if got.Workspace.Root != workspace || got.Workspace.Ref != "acme/payment" || got.Workspace.BaseCommitID != "cmt_state" {
		t.Fatalf("unexpected workspace context: %#v", got.Workspace)
	}
	if got.ActiveSlice != "acme/payment" || got.ActiveSliceSource != "workspace" {
		t.Fatalf("unexpected active slice: %#v", got)
	}
}

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

func TestEnvironmentAliases(t *testing.T) {
	t.Setenv("GS_SERVER_ADDR", "127.0.0.1:60001")
	t.Setenv("GITSLICE_GRPC_ADDR", "127.0.0.1:50051")
	if got := defaultServerAddr(); got != "127.0.0.1:60001" {
		t.Fatalf("defaultServerAddr = %q, want GS_SERVER_ADDR", got)
	}

	t.Setenv("GS_WEB_URL", "http://127.0.0.1:60002")
	t.Setenv("GITSLICE_WEB_URL", "http://127.0.0.1:5173")
	if got := defaultWebURL(); got != "http://127.0.0.1:60002" {
		t.Fatalf("defaultWebURL = %q, want GS_WEB_URL", got)
	}

	t.Setenv("GS_GATEWAY_URL", "http://127.0.0.1:60003")
	t.Setenv("GITSLICE_GATEWAY_URL", "http://127.0.0.1:8082")
	if got := defaultGatewayURL(); got != "http://127.0.0.1:60003" {
		t.Fatalf("defaultGatewayURL = %q, want GS_GATEWAY_URL", got)
	}
}

func TestBrowsePrintsWebURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "root",
			args: []string{"browse", "--web-url", "127.0.0.1:8082", "--print"},
			want: "http://127.0.0.1:8082/",
		},
		{
			name: "route",
			args: []string{"browse", "signup", "--web-url", "http://127.0.0.1:8082", "--print"},
			want: "http://127.0.0.1:8082/signup",
		},
		{
			name: "base path and query",
			args: []string{"browse", "slices?account=nic", "--web-url", "https://web.example.invalid/ui/", "--print"},
			want: "https://web.example.invalid/ui/slices?account=nic",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
			if err := r.Run(context.Background(), tc.args); err != nil {
				t.Fatalf("browse failed: %v\nstderr:\n%s", err, stderr.String())
			}
			if strings.TrimSpace(stdout.String()) != tc.want {
				t.Fatalf("browse URL = %q, want %q", strings.TrimSpace(stdout.String()), tc.want)
			}
		})
	}
}

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

func TestAuthSignupStoresCallbackToken(t *testing.T) {
	home := t.TempDir()
	stdoutReader, stdoutWriter := io.Pipe()
	var stderr bytes.Buffer
	r := Runner{Home: home, Stdout: stdoutWriter, Stderr: &stderr}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		err := r.Run(ctx, []string{
			"auth", "signup",
			"--username", "New_User",
			"--server", "127.0.0.1:50051",
			"--web-url", "http://signup.example.invalid",
			"--no-browser",
		})
		_ = stdoutWriter.Close()
		errCh <- err
	}()

	approvalURL := readSignupApprovalURL(t, stdoutReader)
	go io.Copy(io.Discard, stdoutReader)
	parsed, err := url.Parse(approvalURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("username") != "New_User" {
		t.Fatalf("approval username = %q, want New_User", query.Get("username"))
	}
	callbackURL := query.Get("callback_url")
	state := query.Get("state")
	if callbackURL == "" || state == "" {
		t.Fatalf("approval URL missing callback/state: %s", approvalURL)
	}
	callback, err := url.Parse(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	callbackQuery := callback.Query()
	callbackQuery.Set("token", "callback-token")
	callbackQuery.Set("subject_id", "user_new_user")
	callbackQuery.Set("state", state)
	callback.RawQuery = callbackQuery.Encode()
	resp, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", resp.StatusCode)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("signup failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var cfg UserConfig
	if err := readJSONFile(filepath.Join(home, ".gitslice", "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ServerAddr != "127.0.0.1:50051" || cfg.Token != "callback-token" || cfg.SubjectID != "user_new_user" {
		t.Fatalf("unexpected stored signup config: %#v", cfg)
	}
}

func TestInvalidFormatReturnsStructuredCommandError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"status", "--format", "yaml"})
	if err == nil {
		t.Fatal("status with invalid format unexpectedly succeeded")
	}
	var cmdErr commandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected commandError, got %T: %v", err, err)
	}
	if cmdErr.Code != "invalid_format" {
		t.Fatalf("unexpected error code %q", cmdErr.Code)
	}
}

func TestResolveSliceRefInputAcceptsBareSignedInSlice(t *testing.T) {
	r := Runner{}
	ref, err := r.resolveSliceRefInput(context.Background(), UserConfig{SubjectID: "user_alice"}, nil, "Payment_API")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Account != "alice" || ref.Slice != "payment-api" {
		t.Fatalf("unexpected bare slice ref: %#v", ref)
	}

	explicit, err := r.resolveSliceRefInput(context.Background(), UserConfig{SubjectID: "user_alice"}, nil, "acme/Payment_API")
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Account != "acme" || explicit.Slice != "payment-api" {
		t.Fatalf("unexpected explicit slice ref: %#v", explicit)
	}
}

func TestResolveSliceRefInputRejectsBareSliceWithoutAccount(t *testing.T) {
	r := Runner{}
	_, err := r.resolveSliceRefInput(context.Background(), UserConfig{}, nil, "payment")
	if err == nil {
		t.Fatal("expected account_required error")
	}
	if !isUserErrorCode(err, "account_required") {
		t.Fatalf("expected account_required, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "account/slice") || !strings.Contains(err.Error(), "gs auth status") {
		t.Fatalf("expected bare slice recovery hint, got:\n%v", err)
	}
}

func TestServerShellCompletionCompletesCommands(t *testing.T) {
	sh := &serverShell{}
	got := sh.completeLine(context.Background(), "c")
	for _, want := range []string{"cat ", "cd "} {
		if !stringSliceContains(got, want) {
			t.Fatalf("command completions missing %q: %#v", want, got)
		}
	}
}

func TestServerShellCompletionCompletesRelativePaths(t *testing.T) {
	sh := &serverShell{
		repo: newFakeShellRepoClient(map[string][]*corev1.TreeEntry{
			"/file-user": {
				{Path: "/file-user/docs", Name: "docs", Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY},
				{Path: "/file-user/notes.txt", Name: "notes.txt", Kind: corev1.EntryKind_ENTRY_KIND_FILE},
			},
			"/file-user/docs": {
				{Path: "/file-user/docs/readme.md", Name: "readme.md", Kind: corev1.EntryKind_ENTRY_KIND_FILE},
			},
		}),
		root:     "/file-user",
		cwd:      "/file-user",
		commitID: "commit_test",
		scope:    "file-user/home",
		scoped:   true,
	}
	if got := sh.completeLine(context.Background(), "cd d"); !stringSliceContains(got, "cd docs/") {
		t.Fatalf("cd path completions = %#v, want cd docs/", got)
	}
	if got := sh.completeLine(context.Background(), "cat n"); !stringSliceContains(got, "cat notes.txt") {
		t.Fatalf("cat path completions = %#v, want cat notes.txt", got)
	}
	if got := sh.completeLine(context.Background(), "write docs/readme.md h"); len(got) != 0 {
		t.Fatalf("write text argument should not complete paths: %#v", got)
	}
}

func TestServerShellCompletionUsesProjectionAncestors(t *testing.T) {
	sh := &serverShell{
		repo:       newFakeShellRepoClient(nil),
		root:       "/",
		cwd:        "/",
		commitID:   "commit_test",
		scope:      "nic4/new-slice",
		scoped:     true,
		projection: []string{"/nic4/tests"},
	}
	if got := sh.completeLine(context.Background(), "cd n"); !stringSliceContains(got, "cd nic4/") {
		t.Fatalf("root projection completions = %#v, want cd nic4/", got)
	}
	if got := sh.completeLine(context.Background(), "cd nic4/t"); !stringSliceContains(got, "cd nic4/tests/") {
		t.Fatalf("nested projection completions = %#v, want cd nic4/tests/", got)
	}
}

func TestAttachBlobIDsReusesServerBlobStatus(t *testing.T) {
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("package payment\n")
	cached, err := cache.PutBytes(content)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeBlobClient{
		status: map[string]*corev1.BlobRecord{
			cached.ContentHash: {Id: "blob_existing", ContentHash: cached.ContentHash, State: "available"},
		},
	}
	edits := []*corev1.FileEdit{
		{Op: "upsert", Path: "/acme/payment/a.go", ContentHash: cached.ContentHash},
		{Op: "upsert", Path: "/acme/payment/b.go", ContentHash: cached.ContentHash},
	}

	if err := attachBlobIDs(context.Background(), client, cache, edits); err != nil {
		t.Fatal(err)
	}
	if client.uploads != 0 {
		t.Fatalf("expected no uploads, got %d", client.uploads)
	}
	for _, edit := range edits {
		if edit.BlobId != "blob_existing" {
			t.Fatalf("expected existing blob id, got %q", edit.BlobId)
		}
	}
}

func TestAttachBlobIDsUploadsEachMissingHashOnceFromCache(t *testing.T) {
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("package payment\nconst Created = true\n")
	cached, err := cache.PutBytes(content)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeBlobClient{status: map[string]*corev1.BlobRecord{}}
	edits := []*corev1.FileEdit{
		{Op: "upsert", Path: "/acme/payment/a.go", ContentHash: cached.ContentHash},
		{Op: "upsert", Path: "/acme/payment/b.go", ContentHash: cached.ContentHash},
	}

	if err := attachBlobIDs(context.Background(), client, cache, edits); err != nil {
		t.Fatal(err)
	}
	if client.uploads != 1 {
		t.Fatalf("expected one upload, got %d", client.uploads)
	}
	wantBlobID := objectid.BlobID(content)
	for _, edit := range edits {
		if edit.BlobId != wantBlobID {
			t.Fatalf("expected uploaded blob id %q, got %q", wantBlobID, edit.BlobId)
		}
	}
}

type fakeBlobClient struct {
	corev1.BlobServiceClient
	status  map[string]*corev1.BlobRecord
	uploads int
}

type fakeShellRepoClient struct {
	corev1.RepositoryServiceClient
	entries map[string]*corev1.TreeEntry
	dirs    map[string][]*corev1.TreeEntry
}

func newFakeShellRepoClient(dirs map[string][]*corev1.TreeEntry) *fakeShellRepoClient {
	entries := map[string]*corev1.TreeEntry{}
	for dir, children := range dirs {
		entries[dir] = &corev1.TreeEntry{
			Path: dir,
			Name: pathBaseForTest(dir),
			Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY,
		}
		for _, child := range children {
			if child != nil {
				entries[child.Path] = child
			}
		}
	}
	return &fakeShellRepoClient{entries: entries, dirs: dirs}
}

func (f *fakeShellRepoClient) ResolvePath(ctx context.Context, req *corev1.ResolvePathRequest, opts ...grpc.CallOption) (*corev1.ResolvePathResponse, error) {
	entry := f.entries[req.Path]
	if entry == nil {
		return nil, status.Error(codes.NotFound, "path not found")
	}
	return &corev1.ResolvePathResponse{Entry: entry}, nil
}

func (f *fakeShellRepoClient) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest, opts ...grpc.CallOption) (*corev1.ListDirectoryResponse, error) {
	entries, ok := f.dirs[req.Path]
	if !ok {
		return nil, status.Error(codes.NotFound, "path not found")
	}
	return &corev1.ListDirectoryResponse{Entries: entries}, nil
}

func pathBaseForTest(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	if slash := strings.LastIndex(p, "/"); slash >= 0 {
		return p[slash+1:]
	}
	return p
}

func (f *fakeBlobClient) GetBlobStatus(ctx context.Context, req *corev1.GetBlobStatusRequest, opts ...grpc.CallOption) (*corev1.GetBlobStatusResponse, error) {
	out := make([]*corev1.BlobRecord, 0, len(req.ContentHashes))
	for _, hash := range req.ContentHashes {
		if record := f.status[hash]; record != nil {
			out = append(out, record)
			continue
		}
		out = append(out, &corev1.BlobRecord{ContentHash: hash, State: "missing"})
	}
	return &corev1.GetBlobStatusResponse{Blobs: out}, nil
}

func (f *fakeBlobClient) UploadBlob(ctx context.Context, req *corev1.UploadBlobRequest, opts ...grpc.CallOption) (*corev1.UploadBlobResponse, error) {
	f.uploads++
	return &corev1.UploadBlobResponse{
		BlobId:      objectid.BlobID(req.Data),
		ContentHash: objectid.RawContentHash(req.Data),
		Size:        int64(len(req.Data)),
	}, nil
}

func readSignupApprovalURL(t *testing.T, r io.Reader) string {
	t.Helper()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("approval URL was not printed")
	return ""
}

type fakeAuthStatusServer struct {
	subjectID string
}

func (f fakeAuthStatusServer) GetAuthStatus(ctx context.Context, req *corev1.GetAuthStatusRequest) (*corev1.GetAuthStatusResponse, error) {
	return &corev1.GetAuthStatusResponse{SubjectId: f.subjectID}, nil
}

func startFakeAuthStatusServer(t *testing.T, validToken, subjectID string) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		values := md.Get("authorization")
		if len(values) != 1 || values[0] != "Bearer "+validToken {
			return nil, status.Error(codes.Unauthenticated, "invalid token")
		}
		return handler(ctx, req)
	}))
	corev1.RegisterAuthServiceServer(server, fakeAuthStatusServer{subjectID: subjectID})
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(lis)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = lis.Close()
		if err := <-errCh; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("fake auth status server failed: %v", err)
		}
	})
	return lis.Addr().String()
}
