package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/checkexec"
	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/net/websocket"
	"google.golang.org/grpc"
)

func TestCodexTurnCategorizesAndFinishes(t *testing.T) {
	var events []RuntimeEvent
	turn := NewCodexTurn(func(ev RuntimeEvent) {
		events = append(events, ev)
	})
	turn.HandleNotification("item/agentMessage/delta", json.RawMessage(`{"itemId":"m1","delta":"Hel"}`))
	turn.HandleNotification("item/completed", json.RawMessage(`{"item":{"id":"m1","type":"agentMessage","text":"Hello world"}}`))
	turn.HandleNotification("item/completed", json.RawMessage(`{"item":{"id":"t1","type":"commandExecution","command":"go test ./..."}}`))
	turn.HandleNotification("turn/completed", json.RawMessage(`{}`))

	select {
	case err := <-turn.Done:
		if err != nil {
			t.Fatalf("turn.Done error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for turn completion")
	}
	if len(events) != 3 {
		t.Fatalf("events len = %d, want 3: %+v", len(events), events)
	}
	if !events[0].Ephemeral || events[0].Type != "message_delta" || events[0].Text != "Hel" {
		t.Fatalf("delta event = %+v", events[0])
	}
	if events[1].Type != "message" || events[1].Text != "Hello world" {
		t.Fatalf("message event = %+v", events[1])
	}
	if events[2].Role != "tool" || events[2].Type != "tool_call" || events[2].Text != "go test ./..." {
		t.Fatalf("tool event = %+v", events[2])
	}
}

func TestWorkspaceInstructionsAndMarkdownLinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gs", "slice.json"), []byte(`{"included_paths":["/acme/app"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := ConversationIncludedPaths(dir)
	if len(paths) != 1 || paths[0] != "/acme/app" {
		t.Fatalf("ConversationIncludedPaths = %v", paths)
	}
	inst := WorkspaceInstructions(paths)
	if !strings.Contains(inst, "/acme/app/") || !strings.Contains(inst, "gsfile:") {
		t.Fatalf("unexpected instructions: %s", inst)
	}

	target := filepath.ToSlash(filepath.Join(dir, "acme", "app", "main.go"))
	ev := &corev1.AgentEvent{
		Role: "agent",
		Type: "message",
		Text: "Updated [main.go](" + target + "#L12) and [ext](https://example.com).",
	}
	NormalizeWorkspaceEventLinks(ev, dir)
	want := "Updated [main.go](gsfile:acme/app/main.go#L12) and [ext](https://example.com)."
	if ev.Text != want {
		t.Fatalf("NormalizeWorkspaceEventLinks = %q, want %q", ev.Text, want)
	}
}

func TestDoorbellListenerWakeAndOrigin(t *testing.T) {
	srv := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		_ = websocket.Message.Send(conn, "wake")
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bell := ListenDoorbell(ctx, srv.URL, "tok-1", nil)
	defer bell.Stop()
	bell.WaitReady(ctx, 2*time.Second)

	select {
	case <-bell.Woken():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for doorbell wake")
	}
	if got := OriginForDoorbell("wss://bell.gitslice.io/wait?x=1"); got != "https://bell.gitslice.io" {
		t.Fatalf("OriginForDoorbell = %q", got)
	}
}

type fakeRepo struct {
	files map[string][]byte
}

func (f fakeRepo) ListDirectory(_ context.Context, req *corev1.ListDirectoryRequest, _ ...grpc.CallOption) (*corev1.ListDirectoryResponse, error) {
	if req.GetPath() != "/acme" && req.GetPath() != "/" {
		return &corev1.ListDirectoryResponse{}, nil
	}
	return &corev1.ListDirectoryResponse{
		Entries: []*corev1.TreeEntry{
			{Path: "/acme/hello.txt", Name: "hello.txt", Kind: corev1.EntryKind_ENTRY_KIND_FILE, Mode: 0o100644, Size: 6},
		},
	}, nil
}

func (f fakeRepo) ReadFile(_ context.Context, req *corev1.ReadFileRequest, _ ...grpc.CallOption) (*corev1.ReadFileResponse, error) {
	return &corev1.ReadFileResponse{Data: f.files[req.GetPath()]}, nil
}

func TestMaterializeCheckTreeAndOutbox(t *testing.T) {
	dest := t.TempDir()
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewCheckTreeRepoClient(fakeRepo{files: map[string][]byte{"/acme/hello.txt": []byte("hello\n")}}, "acme")
	if err := MaterializeCheckTree(context.Background(), repo, cache, nil, CheckTreeWriter{}, "tree-1", []string{"/"}, dest); err != nil {
		t.Fatalf("MaterializeCheckTree: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "hello.txt"))
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("materialized file = %q, err = %v", got, err)
	}

	outbox := &CheckRunOutbox{}
	outbox.AppendLocked(&corev1.CheckRunUpdate{RunId: "r1", Status: "running", ClientSeq: 1})
	outbox.AppendLocked(&corev1.CheckRunUpdate{RunId: "r1", Status: "passed", ClientSeq: 2, Final: true})
	if outbox.AckLocked(1) {
		t.Fatal("expected outbox not drained after seq 1")
	}
	if !outbox.AckLocked(2) {
		t.Fatal("expected outbox drained after terminal seq 2")
	}
}

func TestDoctorAndServiceRendering(t *testing.T) {
	checks := RunDoctorChecks(context.Background(), true, DoctorConfig{
		Token:      "gsk_live",
		SubjectID:  "agent_1",
		ConfigPath: "/tmp/config.json",
	}, DoctorProbes{
		DetectRuntime: func(context.Context) checkexec.RuntimeInfo {
			return checkexec.RuntimeInfo{Name: "docker", Rootless: true}
		},
		CacheRoot:     func() (string, error) { return t.TempDir(), nil },
		FreeBytes:     func(string) (uint64, error) { return 20 * 1024 * 1024 * 1024, nil },
		LookPath:      func(name string) (string, error) { return "/usr/bin/" + name, nil },
		CommandOutput: func(_ context.Context, name string, _ ...string) ([]byte, error) { return []byte(name + " 1.0"), nil },
	})
	if err := DoctorFailure(checks); err != nil {
		t.Fatalf("DoctorFailure: %v (%+v)", err, checks)
	}

	unit := RenderSystemdServiceWithSlices("ci-1", "/usr/local/bin/gs", "/home/dev", true, []string{"acme/app"}, map[string]string{"HOME": "/home/dev"})
	if !strings.Contains(unit, "--ci") || !strings.Contains(unit, "acme/app") {
		t.Fatalf("unexpected systemd unit:\n%s", unit)
	}
	plist := RenderLaunchdServiceWithSlices("ci-1", "/usr/local/bin/gs", "/Users/dev", true, nil, map[string]string{"HOME": "/Users/dev"}, "/Users/dev/agent.log")
	if !strings.Contains(plist, "io.gitslice.agent.ci-1") {
		t.Fatalf("unexpected launchd plist:\n%s", plist)
	}

	var uerr interface {
		UserError() (string, string, string)
	}
	if !errors.As(NewError("bad", "msg", "hint"), &uerr) {
		t.Fatal("NewError must implement UserError")
	}
}

func TestRenderTranscriptAndSanitizeEnv(t *testing.T) {
	tr := RenderTranscript([]*corev1.ConversationEvent{
		{Role: "user", Type: "message", Text: "fix bug"},
		{Role: "agent", Type: "message_delta", Text: "fix"},
		{Role: "agent", Type: "message", Text: "fixed"},
	})
	if !strings.Contains(tr, "[User] fix bug") || !strings.Contains(tr, "[Assistant] fixed") || strings.Contains(tr, "[Assistant] fix\n") {
		t.Fatalf("unexpected transcript:\n%s", tr)
	}
	env := SanitizedRuntimeEnv([]string{"PATH=/bin", "GS_TOKEN=secret", "DATABASE_URL=postgres://x"})
	if len(env) != 1 || env[0] != "PATH=/bin" {
		t.Fatalf("SanitizedRuntimeEnv = %v", env)
	}
}
