package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	for _, want := range []string{"gs auth token", "gs auth logout", "gs alias list", "gs alias set <name> <command>", "gs browse [web-path]", "gs init <slice|account:slice>", "gs import <source>", "gs sync", "gs workspace sync", "gs ci", "gs create", "gs modify", "gs submit [changeset]", "gs deps [dependency-tree]", "gs update-dependents [changeset]", "gs switch <changeset>", "gs up [changeset]", "gs down [steps]", "gs top", "gs bottom", "gs move <changeset> --onto <base|root>", "gs insert --base <changeset> --message <title>", "gs detach <changeset>", "gs log [-- <path>]", "gs show <commit-id-or-prefix>", "gs version", "gs completion <shell>", "gs fs ls [remote-path]", "gs fs cat <absolute-path>", "gs fs mkdir <absolute-path>", "gs help <topic>"} {
		if !uses[want] {
			t.Fatalf("schema missing %q", want)
		}
	}
	for _, removed := range []string{"gs repo import github <owner/repo-or-url>", "gs repository import github <owner/repo-or-url>", "gs cs create", "gs cs update", "gs cs submit [changeset]", "gs cs status [changeset]", "gs cs diff [changeset]", "gs cs list"} {
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

func TestRunCIPassingHostCheck(t *testing.T) {
	workspace := t.TempDir()
	writeCIWorkspaceForTest(t, workspace, "true")

	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"ci"}); err != nil {
		t.Fatalf("gs ci failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "acme/payment/check: passed") || !strings.Contains(got, "run ") {
		t.Fatalf("gs ci stdout missing passing check/timing:\n%s", got)
	}
}

func TestRunCIFailingHostCheckExitsNonZero(t *testing.T) {
	workspace := t.TempDir()
	writeCIWorkspaceForTest(t, workspace, "false")

	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"ci"})
	if err == nil {
		t.Fatalf("gs ci unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "acme/payment/check: failed") || !strings.Contains(got, "exit 1") {
		t.Fatalf("gs ci stdout missing failing check summary:\n%s", got)
	}
}

func writeCIWorkspaceForTest(t *testing.T, workspace, checkRun string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:       "acme",
		Slice:         "payment",
		IncludedPaths: []string{"/acme/payment"},
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	checksDir := filepath.Join(workspace, "acme", "payment", ".gitslice")
	if err := os.MkdirAll(checksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	checksYAML := "version: 1\nchecks:\n  check:\n    run: " + strconv.Quote(checkRun) + "\n"
	if err := os.WriteFile(filepath.Join(checksDir, "checks.yaml"), []byte(checksYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "acme", "payment", "app.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredCommandMessageRejectsNonInteractiveEmptyValue(t *testing.T) {
	r := Runner{Stdin: strings.NewReader("ignored\n")}
	value, err := r.requiredCommandMessage(commandOptions{NonInteractive: true}, "", "Title: ", "create requires --message", "Run gs create --message <title>.")
	if err == nil {
		t.Fatal("expected message_required error")
	}
	if value != "" {
		t.Fatalf("value = %q, want empty", value)
	}
	var cmdErr commandError
	if !errors.As(err, &cmdErr) || cmdErr.Code != "message_required" {
		t.Fatalf("error = %#v, want message_required commandError", err)
	}
	value, err = r.requiredCommandMessage(commandOptions{NonInteractive: true}, "  explicit title  ", "Title: ", "create requires --message", "Run gs create --message <title>.")
	if err != nil {
		t.Fatal(err)
	}
	if value != "explicit title" {
		t.Fatalf("value = %q, want trimmed explicit title", value)
	}
}

func TestCanonicalStackCommandsRejectPreStackWorkspaceState(t *testing.T) {
	err := rejectPreStackWorkspaceState(WorkspaceState{
		CurrentChangesetID: "cs_123",
		CurrentPatchsetID:  "ps_123",
		BaseCommitID:       "cmt_123",
	})
	if err == nil {
		t.Fatal("pre-dependency workspace state unexpectedly passed")
	}
	if !strings.Contains(err.Error(), "unsupported pre-dependency format") {
		t.Fatalf("unexpected error: %v", err)
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

func TestApplyFileEditsToSnapshotReplaysStackAncestorEdits(t *testing.T) {
	ws := WorkspaceConfig{
		Account:       "acme",
		Slice:         "payment",
		IncludedPaths: []string{"/acme/payment"},
	}
	base := BaseSnapshot{Files: map[string]BaseSnapshotFile{
		"/acme/payment/a.go":       {Path: "/acme/payment/a.go", RelPath: "a.go", ContentHash: "sha256:a", Mode: 0o100644},
		"/acme/payment/dir/old.go": {Path: "/acme/payment/dir/old.go", RelPath: "dir/old.go", ContentHash: "sha256:old", Mode: 0o100644},
	}}
	edits := []*corev1.FileEdit{
		{Op: "rename", OldPath: "/acme/payment/dir", Path: "/acme/payment/renamed"},
		{Op: "upsert", Path: "/acme/payment/b.go", ContentHash: "sha256:b"},
		{Op: "delete", Path: "/acme/payment/a.go"},
	}

	if err := applyFileEditsToSnapshot(ws, &base, edits); err != nil {
		t.Fatal(err)
	}
	if _, ok := base.Files["/acme/payment/a.go"]; ok {
		t.Fatalf("deleted file remained in snapshot: %#v", base.Files)
	}
	renamed, ok := base.Files["/acme/payment/renamed/old.go"]
	if !ok {
		t.Fatalf("renamed file missing from snapshot: %#v", base.Files)
	}
	if renamed.RelPath != "acme/payment/renamed/old.go" {
		t.Fatalf("renamed rel path = %q", renamed.RelPath)
	}
	added, ok := base.Files["/acme/payment/b.go"]
	if !ok {
		t.Fatalf("added file missing from snapshot: %#v", base.Files)
	}
	if added.Mode != 0o100644 || added.RelPath != "acme/payment/b.go" {
		t.Fatalf("added snapshot file = %#v", added)
	}
}

func TestStackMoveRestacksAndUpdatesWorkspaceState(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackMoveServer()
	serverAddr := startFakeStackMoveServer(t, server)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		ActiveStackID:      "stk_1",
		CurrentChangesetID: "cs_moved",
		CurrentPatchsetID:  "ps_moved_1",
		BaseCommitID:       "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "base_snapshot.json"), BaseSnapshot{
		CommitID: "cmt_base",
		Files:    map[string]BaseSnapshotFile{},
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"move", "cs_moved", "--onto", "cs_parent", "--json"}); err != nil {
		t.Fatalf("move failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if server.reparentReq == nil {
		t.Fatal("ReparentStackEntry was not called")
	}
	if server.reparentReq.NewParentChangesetId != "cs_parent" || server.reparentReq.NewParentPatchsetId != "ps_parent_1" {
		t.Fatalf("unexpected reparent request: %#v", server.reparentReq)
	}
	if server.restackReq == nil {
		t.Fatal("Restack was not called")
	}
	if server.restackReq.StartChangesetId != "cs_moved" {
		t.Fatalf("unexpected restack request: %#v", server.restackReq)
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentChangesetID != "cs_moved" || state.CurrentPatchsetID != "ps_moved_2" {
		t.Fatalf("workspace state was not refreshed after restack: %#v", state)
	}
	var out stackOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("move output is not JSON: %v\n%s", err, stdout.String())
	}
	if !stringSliceContains(out.RestackedChangesets, "cs_moved") {
		t.Fatalf("move output missing restacked changeset: %#v", out)
	}
}

func TestStackDetachRestacksNewStackAndUpdatesWorkspaceState(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackDetachServer()
	serverAddr := startFakeStackDetachServer(t, server)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		ActiveStackID:      "stk_source",
		CurrentChangesetID: "cs_child",
		CurrentPatchsetID:  "ps_child_1",
		BaseCommitID:       "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "base_snapshot.json"), BaseSnapshot{
		CommitID: "cmt_base",
		Files:    map[string]BaseSnapshotFile{},
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"detach", "cs_child", "--message", "detached child stack", "--json"}); err != nil {
		t.Fatalf("detach failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if server.detachReq == nil {
		t.Fatal("DetachStackEntry was not called")
	}
	if server.detachReq.StackId != "stk_source" || server.detachReq.ChangesetId != "cs_child" || server.detachReq.Title != "detached child stack" {
		t.Fatalf("unexpected detach request: %#v", server.detachReq)
	}
	if server.restackReq == nil {
		t.Fatal("Restack was not called")
	}
	if server.restackReq.StackId != "stk_detached" || server.restackReq.StartChangesetId != "cs_child" || server.restackReq.TargetBaseCommitId != "cmt_base" {
		t.Fatalf("unexpected restack request: %#v", server.restackReq)
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveStackID != "stk_detached" || state.CurrentChangesetID != "cs_child" || state.CurrentPatchsetID != "ps_child_2" {
		t.Fatalf("workspace state was not refreshed after detach: %#v", state)
	}
	var out stackOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("detach output is not JSON: %v\n%s", err, stdout.String())
	}
	if out.StackID != "stk_detached" || !stringSliceContains(out.RestackedChangesets, "cs_child") {
		t.Fatalf("detach output missing detached stack/restacked changeset: %#v", out)
	}
}

func TestStackRestackWritesConflictStateAndSwitchesActiveEntry(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	serverAddr := startFakeStackRestackConflictServer(t)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		ActiveStackID:      "stk_1",
		CurrentChangesetID: "cs_root",
		CurrentPatchsetID:  "ps_root_1",
		BaseCommitID:       "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "base_snapshot.json"), BaseSnapshot{
		CommitID: "cmt_base",
		Files:    map[string]BaseSnapshotFile{},
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"update-dependents", "cs_child"}); err != nil {
		t.Fatalf("update-dependents failed: %v\nstderr:\n%s", err, stderr.String())
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentChangesetID != "cs_child" || state.CurrentPatchsetID != "ps_child_2" {
		t.Fatalf("workspace state did not switch to conflicted entry: %#v", state)
	}
	conflictState, ok, err := r.readWorkspaceConflictState()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || conflictState.ChangesetID != "cs_child" || conflictState.PatchsetID != "ps_child_2" || len(conflictState.Conflicts) != 1 {
		t.Fatalf("unexpected conflict state: ok=%v state=%#v", ok, conflictState)
	}
	if conflictState.Conflicts[0].Path != "/acme/payment/conflict.go" || conflictState.Conflicts[0].ConflictClass != "restack" {
		t.Fatalf("unexpected conflict record: %#v", conflictState.Conflicts[0])
	}
	marker, err := os.ReadFile(filepath.Join(workspace, "acme", "payment", "conflict.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<<<<<<< gitslice update local",
		"(local side content was not returned)",
		"||||||| gitslice update base",
		"=======",
		">>>>>>> gitslice update remote",
	} {
		if !strings.Contains(string(marker), want) {
			t.Fatalf("conflict marker missing %q:\n%s", want, string(marker))
		}
	}
	if !strings.Contains(stdout.String(), "conflicts: child") {
		t.Fatalf("restack output missing conflict notice:\n%s", stdout.String())
	}
}

func TestWorkspaceSyncRestacksActiveStackOnCleanBaseAdvance(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackMoveServer()
	serverAddr := startFakeStackSyncServer(t, server, "cmt_new")
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		ActiveStackID:      "stk_1",
		CurrentChangesetID: "cs_moved",
		CurrentPatchsetID:  "ps_moved_1",
		BaseCommitID:       "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "base_snapshot.json"), BaseSnapshot{
		CommitID: "cmt_base",
		Files:    map[string]BaseSnapshotFile{},
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"sync", "--json"}); err != nil {
		t.Fatalf("sync failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if server.restackReq == nil {
		t.Fatal("Restack was not called")
	}
	if server.restackReq.TargetBaseCommitId != "cmt_new" {
		t.Fatalf("unexpected restack request: %#v", server.restackReq)
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		t.Fatal(err)
	}
	if state.BaseCommitID != "cmt_new" || state.CurrentPatchsetID != "ps_moved_2" {
		t.Fatalf("workspace state was not refreshed after sync restack: %#v", state)
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if ws.BaseCommitID != "cmt_new" {
		t.Fatalf("workspace config base = %q", ws.BaseCommitID)
	}
	base, err := r.readBaseSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if base.CommitID != "cmt_new" {
		t.Fatalf("base snapshot commit = %q", base.CommitID)
	}
	var out workspaceSyncOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("sync output is not JSON: %v\n%s", err, stdout.String())
	}
	if !stringSliceContains(out.RestackedChangesets, "cs_moved") {
		t.Fatalf("sync output missing restacked changeset: %#v", out)
	}
}

func TestStackUpRequiresExplicitChildWhenMultipleChildren(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackMoveServer()
	serverAddr := startFakeStackMoveServer(t, server)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	writeStackWorkspaceForTest(t, workspace, "stk_1", "cs_root", "ps_root_1")

	err := r.Run(context.Background(), []string{"up"})
	if !isUserErrorCode(err, "ambiguous_stack_navigation") {
		t.Fatalf("up err = %v, want ambiguous_stack_navigation", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("ambiguous up wrote stdout:\n%s", stdout.String())
	}
}

func TestStackCommandJSONUsesStackTreeSchema(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackMoveServer()
	serverAddr := startFakeStackMoveServer(t, server)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	writeStackWorkspaceForTest(t, workspace, "stk_1", "cs_moved", "ps_moved_1")

	if err := r.Run(context.Background(), []string{"deps", "--json"}); err != nil {
		t.Fatalf("deps --json failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var out stackOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stack output is not JSON: %v\n%s", err, stdout.String())
	}
	if out.StackID != "stk_1" || out.ActiveChangesetID != "cs_moved" || out.RootChangesetID != "cs_root" || len(out.Entries) != 3 {
		t.Fatalf("unexpected stack JSON: %#v", out)
	}
	child := out.Entries[1]
	if child.ChangesetID != "cs_parent" || child.ParentChangesetID != "cs_root" || child.ParentPatchsetID != "ps_root_1" || child.Depth != 1 || child.DisplayOrder != 2 {
		t.Fatalf("unexpected child stack entry JSON: %#v", child)
	}
}

func TestStackSubmitNoWatchAndPolling(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackSubmitServer()
	serverAddr := startFakeStackSubmitServer(t, server)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	writeStackWorkspaceForTest(t, workspace, "stk_1", "cs_root", "ps_root_1")

	if err := r.Run(context.Background(), []string{"submit", "--with-dependencies", "--no-watch", "--json"}); err != nil {
		t.Fatalf("submit --with-dependencies --no-watch failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if server.submitReq == nil || server.submitReq.StackId != "stk_1" {
		t.Fatalf("unexpected no-watch submit request: %#v", server.submitReq)
	}
	var noWatchOut struct {
		DependencyID string `json:"dependency_id"`
		Status       string `json:"status"`
		Results      []struct {
			ChangesetID string `json:"changeset_id"`
			Status      string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &noWatchOut); err != nil {
		t.Fatalf("submit no-watch output is not JSON: %v\n%s", err, stdout.String())
	}
	if noWatchOut.DependencyID != "stk_1" || noWatchOut.Status != "submitted" || len(noWatchOut.Results) != 1 || noWatchOut.Results[0].Status != "pending_publish" {
		t.Fatalf("unexpected no-watch submit output: %#v", noWatchOut)
	}

	stdout.Reset()
	stderr.Reset()
	server.submitReq = nil
	server.getChangesetCalls = 0
	server.getRefCalls = 0
	if err := r.Run(context.Background(), []string{"submit", "--with-dependencies", "--watch-timeout", "200ms"}); err != nil {
		t.Fatalf("submit --with-dependencies with polling failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if server.submitReq == nil || server.submitReq.StackId != "stk_1" {
		t.Fatalf("unexpected watched submit request: %#v", server.submitReq)
	}
	if server.getChangesetCalls == 0 || server.getRefCalls == 0 {
		t.Fatalf("watched submit did not poll changeset/ref: changeset=%d ref=%d", server.getChangesetCalls, server.getRefCalls)
	}
	if !strings.Contains(stdout.String(), "pending_publish") || !strings.Contains(stdout.String(), "root") {
		t.Fatalf("watched submit output missing entry status:\n%s", stdout.String())
	}
}

func writeStackWorkspaceForTest(t *testing.T, workspace, stackID, changesetID, patchsetID string) {
	t.Helper()
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		ActiveStackID:      stackID,
		CurrentChangesetID: changesetID,
		CurrentPatchsetID:  patchsetID,
		BaseCommitID:       "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
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

func TestMergeTextLines(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		local  string
		remote string
		want   string
		ok     bool
	}{
		{
			name:   "non overlapping replacements",
			base:   "a\nb\nc\n",
			local:  "a\nlocal b\nc\n",
			remote: "remote a\nb\nc\n",
			want:   "remote a\nlocal b\nc\n",
			ok:     true,
		},
		{
			name:   "adjacent replacements",
			base:   "a\nb\nc\n",
			local:  "a\nlocal b\nc\n",
			remote: "a\nb\nremote c\n",
			want:   "a\nlocal b\nremote c\n",
			ok:     true,
		},
		{
			name:   "overlapping replacements conflict",
			base:   "a\nb\nc\n",
			local:  "a\nlocal b\nc\n",
			remote: "a\nremote b\nc\n",
			ok:     false,
		},
		{
			name:   "same insertion point conflict",
			base:   "a\nb\n",
			local:  "a\nlocal\nb\n",
			remote: "a\nremote\nb\n",
			ok:     false,
		},
		{
			name:   "identical side edit",
			base:   "a\nb\n",
			local:  "a\nshared\nb\n",
			remote: "a\nshared\nb\n",
			want:   "a\nshared\nb\n",
			ok:     true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mergeTextLines([]byte(tc.base), []byte(tc.local), []byte(tc.remote))
			if ok != tc.ok {
				t.Fatalf("merge ok = %v, want %v", ok, tc.ok)
			}
			if ok && string(got) != tc.want {
				t.Fatalf("merged text mismatch:\nwant:\n%s\ngot:\n%s", tc.want, string(got))
			}
		})
	}
}

func TestApplyWorkspaceSyncMergeStrategies(t *testing.T) {
	const path = "/acme/payment/strategy.go"
	t.Run("manual keeps conflicts", func(t *testing.T) {
		plan := workspaceSyncPlan{
			LocalPaths: []string{path},
			Conflicts:  []WorkspaceConflict{{Path: path}},
		}
		if err := (Runner{}).applyWorkspaceSyncMergeStrategy(context.Background(), nil, nil, "", "", workspaceSyncMergeManual, BaseSnapshot{}, nil, nil, &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Conflicts) != 1 || len(plan.LocalPaths) != 1 {
			t.Fatalf("manual strategy changed plan unexpectedly: %#v", plan)
		}
	})
	t.Run("ours keeps local side", func(t *testing.T) {
		plan := workspaceSyncPlan{
			LocalPaths: []string{path},
			Conflicts:  []WorkspaceConflict{{Path: path}},
		}
		if err := (Runner{}).applyWorkspaceSyncMergeStrategy(context.Background(), nil, nil, "", "", workspaceSyncMergeOurs, BaseSnapshot{}, nil, nil, &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Conflicts) != 0 || !stringSliceContains(plan.LocalPaths, path) || len(plan.RemoteUpserts) != 0 {
			t.Fatalf("ours strategy plan = %#v", plan)
		}
	})
	t.Run("theirs takes remote side", func(t *testing.T) {
		plan := workspaceSyncPlan{
			LocalPaths: []string{path},
			Conflicts:  []WorkspaceConflict{{Path: path}},
		}
		remote := map[string]remoteWorkspaceFile{
			path: {BaseSnapshotFile: BaseSnapshotFile{Path: path}},
		}
		if err := (Runner{}).applyWorkspaceSyncMergeStrategy(context.Background(), nil, nil, "", "", workspaceSyncMergeTheirs, BaseSnapshot{}, nil, remote, &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Conflicts) != 0 || len(plan.LocalPaths) != 0 || len(plan.RemoteUpserts) != 1 || !stringSliceContains(plan.UpdatedPaths, path) {
			t.Fatalf("theirs strategy plan = %#v", plan)
		}
	})
}

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
	if !strings.Contains(err.Error(), "gs auth status") || !strings.Contains(err.Error(), "gs auth login") {
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
	if !strings.Contains(err.Error(), "gs auth login") {
		t.Fatalf("expected login recovery hint, got:\n%v", err)
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
	if got.Workspace.Root != workspace || got.Workspace.Ref != "acme:payment" || got.Workspace.BaseCommitID != "cmt_state" {
		t.Fatalf("unexpected workspace context: %#v", got.Workspace)
	}
	if got.ActiveSlice != "acme:payment" || got.ActiveSliceSource != "workspace" {
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

func TestHostedDefaults(t *testing.T) {
	for _, name := range []string{
		"GS_SERVER_ADDR",
		"GITSLICE_GRPC_ADDR",
		"GITSLICE_SERVER_ADDR",
		"GS_WEB_URL",
		"GITSLICE_WEB_URL",
	} {
		t.Setenv(name, "")
	}

	if got := defaultServerAddr(); got != "api.gitslice.io:443" {
		t.Fatalf("defaultServerAddr = %q, want production API", got)
	}
	if got := defaultWebURL(); got != "https://gitslice.io" {
		t.Fatalf("defaultWebURL = %q, want production web", got)
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

func TestScanWorkspaceFilesIncludesUserDotPaths(t *testing.T) {
	workspace := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("acme/payment/.env", "APP_ENV=test\n")
	write("acme/payment/.github/workflows/ci.yml", "name: ci\n")
	write("acme/payment/src/.keep", "")
	write(".gs/slice.json", "{}")
	write(".gs/state.json", "{}")
	write(".git/config", "[core]\n")
	write(".gitslice/cache", "local\n")

	r := Runner{Home: t.TempDir(), Dir: workspace}
	files, err := r.scanWorkspaceFiles(WorkspaceConfig{
		Account:       "acme",
		Slice:         "payment",
		IncludedPaths: []string{"/acme/payment"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"/acme/payment/.env",
		"/acme/payment/.github/workflows/ci.yml",
		"/acme/payment/src/.keep",
	} {
		if _, ok := files[want]; !ok {
			t.Fatalf("scan missing %s; got paths %#v", want, files)
		}
	}
	for _, notWant := range []string{
		"/acme/payment/.gs/state.json",
		"/acme/payment/.git/config",
		"/acme/payment/.gitslice/cache",
	} {
		if _, ok := files[notWant]; ok {
			t.Fatalf("scan included workspace metadata path %s: %#v", notWant, files[notWant])
		}
	}
}

func TestHashWorkspaceFilesUsesBoundedConcurrency(t *testing.T) {
	paths := []string{"a", "b", "c", "d", "e", "f"}
	started := make(chan struct{}, len(paths))
	release := make(chan struct{})
	var mu sync.Mutex
	current := 0
	maximum := 0

	done := make(chan error, 1)
	go func() {
		objects, err := hashWorkspaceFiles(context.Background(), paths, 3, func(p string) (clientcache.Object, error) {
			mu.Lock()
			current++
			if current > maximum {
				maximum = current
			}
			mu.Unlock()
			started <- struct{}{}
			<-release
			mu.Lock()
			current--
			mu.Unlock()
			return clientcache.Object{ContentHash: objectid.RawContentHash([]byte(p)), Size: int64(len(p))}, nil
		})
		if err == nil && len(objects) != len(paths) {
			err = fmt.Errorf("hashed object count = %d, want %d", len(objects), len(paths))
		}
		done <- err
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for hash worker")
		}
	}
	mu.Lock()
	gotCurrent, gotMaximum := current, maximum
	mu.Unlock()
	if gotCurrent != 3 || gotMaximum != 3 {
		t.Fatalf("hash concurrency current=%d max=%d, want 3", gotCurrent, gotMaximum)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hash workers to finish")
	}
}

func TestWriteCaptureDiagnosticsReportsPhaseTimingsAndCounts(t *testing.T) {
	var stderr bytes.Buffer
	r := Runner{Stderr: &stderr}
	r.writeCaptureDiagnostics(captureDiagnostics{
		Result:       "captured",
		ChangedPaths: 7,
		Snapshot: snapshotEditsStats{
			Scan: workspaceScanStats{
				FileCount:    11,
				ByteCount:    2048,
				HashWorkers:  4,
				WalkDuration: 2 * time.Millisecond,
				HashDuration: 3 * time.Millisecond,
			},
			Blobs: blobAttachStats{
				RequiredBlobs:  6,
				AvailableBlobs: 2,
				UploadedBlobs:  4,
				UploadedBytes:  1024,
				UploadWorkers:  4,
				StatusDuration: time.Millisecond,
				UploadDuration: 5 * time.Millisecond,
			},
			DiffDuration: time.Millisecond,
		},
		ChecksDuration: 6 * time.Millisecond,
		UpdateDuration: 7 * time.Millisecond,
		TotalDuration:  25 * time.Millisecond,
	})

	got := stderr.String()
	for _, want := range []string{
		"result=captured",
		"files=11",
		"hashed_bytes=2048",
		"changed_paths=7",
		"unique_blobs=6",
		"remote_hits=2",
		"uploaded_blobs=4",
		"hash_workers=4",
		"upload_workers=4",
		"walk=2ms",
		"hash=3ms",
		"upload=5ms",
		"checks=6ms",
		"update=7ms",
		"total=25ms",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics missing %q:\n%s", want, got)
		}
	}
}

func TestShouldSkipAgentMetadataOnlyAtWorkspaceRoot(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
	}{
		{rel: ".agent-meta.json", want: true},
		{rel: "sub/.agent-meta.json", want: false},
		{rel: ".agent-meta-abc123.tmp", want: true},
		{rel: ".git/x", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			if got := shouldSkip(tt.rel, nil); got != tt.want {
				t.Fatalf("shouldSkip(%q) = %t, want %t", tt.rel, got, tt.want)
			}
		})
	}
}

func TestResolveSliceRefInputAcceptsExplicitSliceOffline(t *testing.T) {
	r := Runner{}
	explicit, err := r.resolveSliceRefInput(context.Background(), UserConfig{SubjectID: "user_alice"}, nil, "acme/Payment_API")
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Account != "acme" || explicit.Slice != "payment-api" {
		t.Fatalf("unexpected explicit slice ref: %#v", explicit)
	}

	// A bare slug now expands to the signed-in personal account via a server
	// round-trip (the account is no longer derivable from the subject id), so
	// offline resolution without a connection requires an explicit account.
	if _, err := r.resolveSliceRefInput(context.Background(), UserConfig{SubjectID: "user_alice"}, nil, "Payment_API"); !isUserErrorCode(err, "account_required") {
		t.Fatalf("bare slice ref offline = %v, want account_required", err)
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
	if !strings.Contains(err.Error(), "account:slice") || !strings.Contains(err.Error(), "gs auth status") {
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

	if err := attachBlobIDs(context.Background(), client, &corev1.SliceRef{Account: "acme", Slice: "payment"}, cache, edits); err != nil {
		t.Fatal(err)
	}
	if uploads, _, _ := client.uploadSnapshot(); uploads != 0 {
		t.Fatalf("expected no uploads, got %d", uploads)
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

	if err := attachBlobIDs(context.Background(), client, &corev1.SliceRef{Account: "acme", Slice: "payment"}, cache, edits); err != nil {
		t.Fatal(err)
	}
	if uploads, _, _ := client.uploadSnapshot(); uploads != 1 {
		t.Fatalf("expected one upload, got %d", uploads)
	}
	wantBlobID := objectid.BlobID(content)
	for _, edit := range edits {
		if edit.BlobId != wantBlobID {
			t.Fatalf("expected uploaded blob id %q, got %q", wantBlobID, edit.BlobId)
		}
	}
}

func TestAttachBlobIDsUploadsMissingHashesConcurrently(t *testing.T) {
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	edits := make([]*corev1.FileEdit, 0, 6)
	for i := 0; i < 6; i++ {
		cached, err := cache.PutBytes([]byte(fmt.Sprintf("content-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, &corev1.FileEdit{Op: "upsert", Path: fmt.Sprintf("/acme/payment/%d.txt", i), ContentHash: cached.ContentHash})
	}
	client := &fakeBlobClient{
		status:        map[string]*corev1.BlobRecord{},
		uploadStarted: make(chan struct{}, len(edits)),
		uploadRelease: make(chan struct{}),
	}
	type attachResult struct {
		stats blobAttachStats
		err   error
	}
	done := make(chan attachResult, 1)
	go func() {
		stats, err := attachBlobIDsWithStats(context.Background(), client, &corev1.SliceRef{Account: "acme", Slice: "payment"}, cache, edits, 3)
		done <- attachResult{stats: stats, err: err}
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-client.uploadStarted:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for upload worker")
		}
	}
	uploads, current, maximum := client.uploadSnapshot()
	if uploads != 3 || current != 3 || maximum != 3 {
		t.Fatalf("upload concurrency uploads=%d current=%d max=%d, want 3", uploads, current, maximum)
	}
	close(client.uploadRelease)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.stats.RequiredBlobs != 6 || result.stats.UploadedBlobs != 6 || result.stats.UploadWorkers != 3 {
			t.Fatalf("unexpected upload stats: %#v", result.stats)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for uploads to finish")
	}
	for _, edit := range edits {
		if edit.BlobId == "" {
			t.Fatalf("missing blob id for %s", edit.Path)
		}
	}
}

type fakeBlobClient struct {
	corev1.BlobServiceClient
	mu             sync.Mutex
	status         map[string]*corev1.BlobRecord
	uploads        int
	currentUploads int
	maximumUploads int
	uploadStarted  chan struct{}
	uploadRelease  chan struct{}
}

type fakeStackMoveServer struct {
	stackBefore *corev1.ChangesetStack
	stackAfter  *corev1.ChangesetStack
	changesets  map[string]*corev1.Changeset
	reparentReq *corev1.ReparentStackEntryRequest
	restackReq  *corev1.RestackRequest
}

func newFakeStackMoveServer() *fakeStackMoveServer {
	root := fakeStackChangeset("cs_root", "ps_root_1")
	parent := fakeStackChangeset("cs_parent", "ps_parent_1")
	movedBefore := fakeStackChangeset("cs_moved", "ps_moved_1")
	movedAfter := fakeStackChangeset("cs_moved", "ps_moved_2")
	stackBefore := &corev1.ChangesetStack{
		Id:            "stk_1",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_moved",
		RootEntryId:   "cs_root",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_1", ChangesetId: "cs_root", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: root},
			{StackId: "stk_1", ChangesetId: "cs_parent", ParentChangesetId: "cs_root", ParentPatchsetId: "ps_root_1", DisplayOrder: 2, SiblingOrder: 1, Depth: 1, State: "draft", Changeset: parent},
			{StackId: "stk_1", ChangesetId: "cs_moved", ParentChangesetId: "cs_root", ParentPatchsetId: "ps_root_1", DisplayOrder: 3, SiblingOrder: 2, Depth: 1, State: "draft", Changeset: movedBefore},
		},
	}
	stackAfter := &corev1.ChangesetStack{
		Id:            "stk_1",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_moved",
		RootEntryId:   "cs_root",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_1", ChangesetId: "cs_root", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: root},
			{StackId: "stk_1", ChangesetId: "cs_parent", ParentChangesetId: "cs_root", ParentPatchsetId: "ps_root_1", DisplayOrder: 2, SiblingOrder: 1, Depth: 1, State: "draft", Changeset: parent},
			{StackId: "stk_1", ChangesetId: "cs_moved", ParentChangesetId: "cs_parent", ParentPatchsetId: "ps_parent_1", DisplayOrder: 3, SiblingOrder: 1, Depth: 2, State: "draft", Changeset: movedAfter},
		},
	}
	return &fakeStackMoveServer{
		stackBefore: stackBefore,
		stackAfter:  stackAfter,
		changesets: map[string]*corev1.Changeset{
			"cs_root":   root,
			"cs_parent": parent,
			"cs_moved":  movedAfter,
		},
	}
}

func fakeStackChangeset(id, patchsetID string) *corev1.Changeset {
	return &corev1.Changeset{
		Id:                    id,
		StackId:               "stk_1",
		BaseCommitId:          "cmt_base",
		TargetRef:             "refs/global/main",
		Status:                "draft",
		CurrentPatchsetId:     patchsetID,
		CurrentPatchsetNumber: 1,
		Patchsets: []*corev1.Patchset{{
			Id:           patchsetID,
			ChangesetId:  id,
			Number:       1,
			BaseCommitId: "cmt_base",
			BaseKind:     "commit",
			FileEdits:    []*corev1.FileEdit{},
		}},
	}
}

type fakeStackMoveStackService struct {
	corev1.UnimplementedChangesetStackServiceServer
	state *fakeStackMoveServer
}

func (f fakeStackMoveStackService) GetStack(ctx context.Context, req *corev1.GetStackRequest) (*corev1.ChangesetStack, error) {
	if f.state.restackReq != nil {
		return f.state.stackAfter, nil
	}
	return f.state.stackBefore, nil
}

func (f fakeStackMoveStackService) ReparentStackEntry(ctx context.Context, req *corev1.ReparentStackEntryRequest) (*corev1.ChangesetStack, error) {
	f.state.reparentReq = req
	return f.state.stackAfter, nil
}

func (f fakeStackMoveStackService) Restack(ctx context.Context, req *corev1.RestackRequest) (*corev1.RestackResponse, error) {
	f.state.restackReq = req
	return &corev1.RestackResponse{StackId: req.StackId, Status: "clean", Entries: []*corev1.Changeset{f.state.changesets["cs_moved"]}}, nil
}

type fakeStackMoveChangesetService struct {
	corev1.UnimplementedChangesetServiceServer
	state *fakeStackMoveServer
}

func (f fakeStackMoveChangesetService) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	cs := f.state.changesets[req.ChangesetId]
	if cs == nil {
		return nil, status.Error(codes.NotFound, "changeset not found")
	}
	return cs, nil
}

func startFakeStackMoveServer(t *testing.T, state *fakeStackMoveServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	corev1.RegisterChangesetStackServiceServer(server, fakeStackMoveStackService{state: state})
	corev1.RegisterChangesetServiceServer(server, fakeStackMoveChangesetService{state: state})
	t.Cleanup(server.Stop)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("fake stack move server failed: %v", err)
		}
	}()
	return lis.Addr().String()
}

type fakeStackSubmitServer struct {
	submitReq         *corev1.SubmitStackRequest
	getChangesetCalls int
	getRefCalls       int
}

func newFakeStackSubmitServer() *fakeStackSubmitServer {
	return &fakeStackSubmitServer{}
}

type fakeStackSubmitStackService struct {
	corev1.UnimplementedChangesetStackServiceServer
	state *fakeStackSubmitServer
}

func (f fakeStackSubmitStackService) SubmitStack(ctx context.Context, req *corev1.SubmitStackRequest) (*corev1.SubmitStackResponse, error) {
	f.state.submitReq = req
	return &corev1.SubmitStackResponse{
		StackId: req.StackId,
		Status:  "submitted",
		Results: []*corev1.SubmitStackEntryResult{{
			ChangesetId:      "cs_root",
			Status:           "pending_publish",
			PendingPublishId: "pending_root",
		}},
	}, nil
}

type fakeStackSubmitChangesetService struct {
	corev1.UnimplementedChangesetServiceServer
	state *fakeStackSubmitServer
}

func (f fakeStackSubmitChangesetService) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	f.state.getChangesetCalls++
	return &corev1.Changeset{
		Id:                req.ChangesetId,
		StackId:           "stk_1",
		Status:            "submitted",
		CommitId:          "commit_root",
		CurrentPatchsetId: "ps_root_1",
	}, nil
}

type fakeStackSubmitRepositoryService struct {
	corev1.UnimplementedRepositoryServiceServer
	state *fakeStackSubmitServer
}

func (f fakeStackSubmitRepositoryService) GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	f.state.getRefCalls++
	return &corev1.Ref{Name: req.RefName, CommitId: "commit_root"}, nil
}

func startFakeStackSubmitServer(t *testing.T, state *fakeStackSubmitServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	corev1.RegisterChangesetStackServiceServer(server, fakeStackSubmitStackService{state: state})
	corev1.RegisterChangesetServiceServer(server, fakeStackSubmitChangesetService{state: state})
	corev1.RegisterRepositoryServiceServer(server, fakeStackSubmitRepositoryService{state: state})
	t.Cleanup(server.Stop)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("fake stack submit server failed: %v", err)
		}
	}()
	return lis.Addr().String()
}

type fakeStackDetachServer struct {
	sourceBefore   *corev1.ChangesetStack
	sourceAfter    *corev1.ChangesetStack
	detachedBefore *corev1.ChangesetStack
	detachedAfter  *corev1.ChangesetStack
	changesets     map[string]*corev1.Changeset
	detachReq      *corev1.DetachStackEntryRequest
	restackReq     *corev1.RestackRequest
}

func newFakeStackDetachServer() *fakeStackDetachServer {
	root := fakeStackChangesetForStack("stk_source", "cs_root", "ps_root_1")
	childBefore := fakeStackChangesetForStack("stk_source", "cs_child", "ps_child_1")
	childAfter := fakeStackChangesetForStack("stk_detached", "cs_child", "ps_child_2")
	sourceBefore := &corev1.ChangesetStack{
		Id:            "stk_source",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_child",
		RootEntryId:   "cs_root",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_source", ChangesetId: "cs_root", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: root},
			{StackId: "stk_source", ChangesetId: "cs_child", ParentChangesetId: "cs_root", ParentPatchsetId: "ps_root_1", DisplayOrder: 2, SiblingOrder: 1, Depth: 1, State: "draft", Changeset: childBefore},
		},
	}
	sourceAfter := &corev1.ChangesetStack{
		Id:            "stk_source",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_root",
		RootEntryId:   "cs_root",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_source", ChangesetId: "cs_root", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: root},
		},
	}
	detachedBefore := &corev1.ChangesetStack{
		Id:            "stk_detached",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_child",
		RootEntryId:   "cs_child",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_detached", ChangesetId: "cs_child", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "needs_restack", Changeset: childBefore},
		},
	}
	detachedAfter := &corev1.ChangesetStack{
		Id:            "stk_detached",
		BaseCommitId:  "cmt_base",
		Status:        "open",
		ActiveEntryId: "cs_child",
		RootEntryId:   "cs_child",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: "stk_detached", ChangesetId: "cs_child", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: childAfter},
		},
	}
	return &fakeStackDetachServer{
		sourceBefore:   sourceBefore,
		sourceAfter:    sourceAfter,
		detachedBefore: detachedBefore,
		detachedAfter:  detachedAfter,
		changesets: map[string]*corev1.Changeset{
			"cs_root":  root,
			"cs_child": childAfter,
		},
	}
}

func fakeStackChangesetForStack(stackID, id, patchsetID string) *corev1.Changeset {
	cs := fakeStackChangeset(id, patchsetID)
	cs.StackId = stackID
	return cs
}

type fakeStackDetachStackService struct {
	corev1.UnimplementedChangesetStackServiceServer
	state *fakeStackDetachServer
}

func (f fakeStackDetachStackService) GetStack(ctx context.Context, req *corev1.GetStackRequest) (*corev1.ChangesetStack, error) {
	switch req.StackId {
	case "stk_source":
		if f.state.detachReq != nil {
			return f.state.sourceAfter, nil
		}
		return f.state.sourceBefore, nil
	case "stk_detached":
		if f.state.restackReq != nil {
			return f.state.detachedAfter, nil
		}
		return f.state.detachedBefore, nil
	default:
		return nil, status.Error(codes.NotFound, "stack not found")
	}
}

func (f fakeStackDetachStackService) DetachStackEntry(ctx context.Context, req *corev1.DetachStackEntryRequest) (*corev1.DetachStackEntryResponse, error) {
	f.state.detachReq = req
	return &corev1.DetachStackEntryResponse{SourceStack: f.state.sourceAfter, DetachedStack: f.state.detachedBefore}, nil
}

func (f fakeStackDetachStackService) Restack(ctx context.Context, req *corev1.RestackRequest) (*corev1.RestackResponse, error) {
	f.state.restackReq = req
	return &corev1.RestackResponse{StackId: req.StackId, Status: "clean", Entries: []*corev1.Changeset{f.state.changesets["cs_child"]}}, nil
}

type fakeStackDetachChangesetService struct {
	corev1.UnimplementedChangesetServiceServer
	state *fakeStackDetachServer
}

func (f fakeStackDetachChangesetService) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	cs := f.state.changesets[req.ChangesetId]
	if cs == nil {
		return nil, status.Error(codes.NotFound, "changeset not found")
	}
	return cs, nil
}

func startFakeStackDetachServer(t *testing.T, state *fakeStackDetachServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	corev1.RegisterChangesetStackServiceServer(server, fakeStackDetachStackService{state: state})
	corev1.RegisterChangesetServiceServer(server, fakeStackDetachChangesetService{state: state})
	t.Cleanup(server.Stop)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("fake stack detach server failed: %v", err)
		}
	}()
	return lis.Addr().String()
}

type fakeStackRestackConflictService struct {
	corev1.UnimplementedChangesetStackServiceServer
}

func (fakeStackRestackConflictService) Restack(ctx context.Context, req *corev1.RestackRequest) (*corev1.RestackResponse, error) {
	return &corev1.RestackResponse{
		StackId: req.StackId,
		Status:  "conflicts",
		Entries: []*corev1.Changeset{{
			Id:                "cs_child",
			StackId:           req.StackId,
			BaseCommitId:      "cmt_base",
			CurrentPatchsetId: "ps_child_2",
			Patchsets: []*corev1.Patchset{{
				Id:           "ps_child_2",
				ChangesetId:  "cs_child",
				Number:       2,
				BaseCommitId: "cmt_base",
				Conflicts: []*corev1.PatchsetConflict{{
					Path:            "/acme/payment/conflict.go",
					ConflictClass:   "restack",
					OldBaseCommitId: "cmt_base",
					NewBaseCommitId: "cmt_base",
				}},
			}},
		}},
	}, nil
}

func (fakeStackRestackConflictService) GetStack(ctx context.Context, req *corev1.GetStackRequest) (*corev1.ChangesetStack, error) {
	root := &corev1.Changeset{
		Id:                "cs_root",
		StackId:           req.StackId,
		BaseCommitId:      "cmt_base",
		CurrentPatchsetId: "ps_root_1",
		Patchsets: []*corev1.Patchset{{
			Id:           "ps_root_1",
			ChangesetId:  "cs_root",
			Number:       1,
			BaseCommitId: "cmt_base",
		}},
	}
	child := &corev1.Changeset{
		Id:                "cs_child",
		StackId:           req.StackId,
		ParentChangesetId: "cs_root",
		ParentPatchsetId:  "ps_root_1",
		BaseCommitId:      "cmt_base",
		CurrentPatchsetId: "ps_child_2",
		Patchsets: []*corev1.Patchset{{
			Id:           "ps_child_2",
			ChangesetId:  "cs_child",
			Number:       2,
			BaseCommitId: "cmt_base",
			Conflicts: []*corev1.PatchsetConflict{{
				Path:            "/acme/payment/conflict.go",
				ConflictClass:   "restack",
				OldBaseCommitId: "cmt_base",
				NewBaseCommitId: "cmt_base",
			}},
		}},
	}
	return &corev1.ChangesetStack{
		Id:            req.StackId,
		BaseCommitId:  "cmt_base",
		RootEntryId:   "cs_root",
		ActiveEntryId: "cs_child",
		Entries: []*corev1.ChangesetStackEntry{
			{StackId: req.StackId, ChangesetId: "cs_root", DisplayOrder: 1, SiblingOrder: 1, Depth: 0, State: "draft", Changeset: root},
			{StackId: req.StackId, ChangesetId: "cs_child", ParentChangesetId: "cs_root", ParentPatchsetId: "ps_root_1", DisplayOrder: 2, SiblingOrder: 1, Depth: 1, State: "draft", Changeset: child},
		},
	}, nil
}

func startFakeStackRestackConflictServer(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	corev1.RegisterChangesetStackServiceServer(server, fakeStackRestackConflictService{})
	t.Cleanup(server.Stop)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("fake stack restack conflict server failed: %v", err)
		}
	}()
	return lis.Addr().String()
}

type fakeStackSyncRepositoryService struct {
	corev1.UnimplementedRepositoryServiceServer
	commitID string
}

func (f fakeStackSyncRepositoryService) GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	return &corev1.Ref{Name: req.RefName, CommitId: f.commitID}, nil
}

func (f fakeStackSyncRepositoryService) ResolvePath(ctx context.Context, req *corev1.ResolvePathRequest) (*corev1.ResolvePathResponse, error) {
	return nil, status.Error(codes.NotFound, "path not found")
}

func startFakeStackSyncServer(t *testing.T, state *fakeStackMoveServer, commitID string) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	corev1.RegisterChangesetStackServiceServer(server, fakeStackMoveStackService{state: state})
	corev1.RegisterRepositoryServiceServer(server, fakeStackSyncRepositoryService{commitID: commitID})
	t.Cleanup(server.Stop)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("fake stack sync server failed: %v", err)
		}
	}()
	return lis.Addr().String()
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	f.uploads++
	f.currentUploads++
	if f.currentUploads > f.maximumUploads {
		f.maximumUploads = f.currentUploads
	}
	started := f.uploadStarted
	release := f.uploadRelease
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		select {
		case <-ctx.Done():
			f.mu.Lock()
			f.currentUploads--
			f.mu.Unlock()
			return nil, ctx.Err()
		case <-release:
		}
	}
	f.mu.Lock()
	f.currentUploads--
	f.mu.Unlock()
	return &corev1.UploadBlobResponse{
		BlobId:      objectid.BlobID(req.Data),
		ContentHash: objectid.RawContentHash(req.Data),
		Size:        int64(len(req.Data)),
	}, nil
}

func (f *fakeBlobClient) uploadSnapshot() (uploads, current, maximum int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploads, f.currentUploads, f.maximumUploads
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
	corev1.UnimplementedAuthServiceServer
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
