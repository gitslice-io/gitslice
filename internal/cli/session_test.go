package cli

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

func TestSessionCachesServiceClients(t *testing.T) {
	cfg := UserConfig{
		ServerAddr: "localhost:50051",
		Token:      "tok_test_123",
		SubjectID:  "user_alice",
	}
	s := newSession(Runner{}, cfg, nil, nil)
	if s.Config().Token != "tok_test_123" {
		t.Fatalf("Config().Token = %q, want tok_test_123", s.Config().Token)
	}
	if s.Auth() == nil || s.Auth() != s.Auth() {
		t.Fatal("expected cached AuthServiceClient instance")
	}
	if s.Slice() == nil || s.Slice() != s.Slice() {
		t.Fatal("expected cached SliceServiceClient instance")
	}
	if s.Changeset() == nil || s.Changeset() != s.Changeset() {
		t.Fatal("expected cached ChangesetServiceClient instance")
	}
	if s.ChangesetStack() == nil || s.ChangesetStack() != s.ChangesetStack() {
		t.Fatal("expected cached ChangesetStackServiceClient instance")
	}
	if s.Repository() == nil || s.Repository() != s.Repository() {
		t.Fatal("expected cached RepositoryServiceClient instance")
	}
	if s.Blob() == nil || s.Blob() != s.Blob() {
		t.Fatal("expected cached BlobServiceClient instance")
	}
	if s.Agent() == nil || s.Agent() != s.Agent() {
		t.Fatal("expected cached AgentServiceClient instance")
	}
	if s.Check() == nil || s.Check() != s.Check() {
		t.Fatal("expected cached CheckServiceClient instance")
	}
	if s.Secret() == nil || s.Secret() != s.Secret() {
		t.Fatal("expected cached SecretServiceClient instance")
	}
	if s.Automation() == nil || s.Automation() != s.Automation() {
		t.Fatal("expected cached AutomationServiceClient instance")
	}
	if s.Feedback() == nil || s.Feedback() != s.Feedback() {
		t.Fatal("expected cached FeedbackServiceClient instance")
	}
}

func TestSessionResolveSliceRefAndWorkspaceFallback(t *testing.T) {
	home := t.TempDir()
	outsideDir := t.TempDir()
	rOutside := Runner{Home: home, Dir: outsideDir}
	sOutside := newSession(rOutside, UserConfig{SubjectID: "user_alice"}, nil, context.Background())

	ref, err := sOutside.ResolveSliceRef("acme:Payment_API", false)
	if err != nil {
		t.Fatalf("ResolveSliceRef explicit failed: %v", err)
	}
	if ref.GetAccount() != "acme" || ref.GetSlice() != "payment-api" {
		t.Fatalf("ResolveSliceRef explicit = %#v, want acme:payment-api", ref)
	}

	if _, err := sOutside.ResolveSliceRef("", true); !isUserErrorCode(err, "slice_required") {
		t.Fatalf("ResolveSliceRef outside workspace = %v, want slice_required", err)
	}
	if _, err := sOutside.ResolveSliceRefWithOptions("", SliceResolveOptions{
		FallbackToWorkspace: true,
		MissingCode:         "missing_slice",
		MissingMessage:      "changeset list needs a slice outside a workspace",
	}); !isUserErrorCode(err, "missing_slice") {
		t.Fatalf("ResolveSliceRefWithOptions outside workspace = %v, want missing_slice", err)
	}

	wsDir := t.TempDir()
	rWS := Runner{Home: home, Dir: wsDir}
	if err := writeJSONFile(filepath.Join(wsDir, ".gs", "slice.json"), WorkspaceConfig{Account: "acme", Slice: "core"}, 0o644); err != nil {
		t.Fatal(err)
	}
	sWS := newSession(rWS, UserConfig{SubjectID: "user_alice"}, nil, context.Background())
	wsRef, err := sWS.ResolveSliceRef("", true)
	if err != nil {
		t.Fatalf("ResolveSliceRef in workspace failed: %v", err)
	}
	if wsRef.GetAccount() != "acme" || wsRef.GetSlice() != "core" {
		t.Fatalf("ResolveSliceRef in workspace = %#v, want acme:core", wsRef)
	}
}

func TestSessionResolveChangesetTarget(t *testing.T) {
	home := t.TempDir()
	wsDir := t.TempDir()
	r := Runner{Home: home, Dir: wsDir}
	if err := r.writeUserConfig(UserConfig{ServerAddr: "localhost:50051", Token: "tok_abc", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(wsDir, ".gs", "slice.json"), WorkspaceConfig{Account: "acme", Slice: "core"}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.writeWorkspaceState(WorkspaceState{BaseCommitID: "sha256:1111"}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.resolveChangesetTarget(""); !isUserErrorCode(err, "no_current_changeset") {
		t.Fatalf("resolveChangesetTarget empty state = %v, want no_current_changeset", err)
	}

	if err := r.writeWorkspaceState(WorkspaceState{
		BaseCommitID:       "sha256:1111",
		CurrentChangesetID: "cs_abcdef1234567890",
		CurrentPatchsetID:  "ps_1",
	}); err != nil {
		t.Fatal(err)
	}

	cur, err := r.resolveChangesetTarget("")
	if err != nil {
		t.Fatalf("resolveChangesetTarget current failed: %v", err)
	}
	if !cur.HasWorkspace || !cur.UsingWorkspaceCurrent || cur.ChangesetID != "cs_abcdef1234567890" {
		t.Fatalf("unexpected current target: %#v", cur)
	}

	shortMatch, err := r.resolveChangesetTarget("abcdef1234")
	if err != nil {
		t.Fatalf("resolveChangesetTarget short handle failed: %v", err)
	}
	if !shortMatch.HasWorkspace || !shortMatch.UsingWorkspaceCurrent || shortMatch.ChangesetID != "abcdef1234" {
		t.Fatalf("unexpected short-handle target: %#v", shortMatch)
	}

	other, err := r.resolveChangesetTarget("cs_9999999999999999")
	if err != nil {
		t.Fatalf("resolveChangesetTarget explicit other failed: %v", err)
	}
	if !other.HasWorkspace || other.UsingWorkspaceCurrent || other.ChangesetID != "cs_9999999999999999" {
		t.Fatalf("unexpected explicit other target: %#v", other)
	}
}

func TestSessionOptionalSliceAccountAndAuthCtxHelpers(t *testing.T) {
	home := t.TempDir()
	outsideDir := t.TempDir()
	sOutside := newSession(Runner{Home: home, Dir: outsideDir}, UserConfig{Token: "tok_sub"}, nil, context.Background())

	optOutside, err := sOutside.ResolveOptionalSliceRef("")
	if err != nil || optOutside != nil {
		t.Fatalf("ResolveOptionalSliceRef outside workspace = (%#v, %v), want (nil, nil)", optOutside, err)
	}

	acct, err := sOutside.ResolveAccount("Acme_Org")
	if err != nil || acct != "acme-org" {
		t.Fatalf("ResolveAccount explicit = (%q, %v), want acme-org", acct, err)
	}

	wsDir := t.TempDir()
	if err := writeJSONFile(filepath.Join(wsDir, ".gs", "slice.json"), WorkspaceConfig{Account: "acme", Slice: "core"}, 0o644); err != nil {
		t.Fatal(err)
	}
	sWS := newSession(Runner{Home: home, Dir: wsDir}, UserConfig{Token: "tok_sub"}, nil, context.Background())
	optWS, err := sWS.ResolveOptionalSliceRef("")
	if err != nil || optWS.GetAccount() != "acme" || optWS.GetSlice() != "core" {
		t.Fatalf("ResolveOptionalSliceRef in workspace = (%#v, %v), want acme:core", optWS, err)
	}

	creds := bearerTokenCreds{}
	meta, err := creds.GetRequestMetadata(sWS.AuthCtx(context.Background()))
	if err != nil || meta["authorization"] != "Bearer tok_sub" {
		t.Fatalf("AuthCtx bearer metadata = %v (err=%v)", meta, err)
	}
}

func TestRunnerDialSeamInterceptsSessionAndLazyDial(t *testing.T) {
	home := t.TempDir()
	sentinel := errors.New("dial intercepted")
	var dialed []string
	r := Runner{
		Home: home,
		Dir:  t.TempDir(),
		Dial: func(_ context.Context, addr string) (*grpc.ClientConn, error) {
			dialed = append(dialed, addr)
			return nil, sentinel
		},
	}
	if err := r.writeUserConfig(UserConfig{ServerAddr: "example.test:443", Token: "tok_abc", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := r.withSession(context.Background(), func(*Session) error { return nil }); !errors.Is(err, sentinel) {
		t.Fatalf("withSession err = %v, want %v", err, sentinel)
	}
	if _, err := r.dialServerLazy(context.Background(), "lazy.test:443"); !errors.Is(err, sentinel) {
		t.Fatalf("dialServerLazy err = %v, want %v", err, sentinel)
	}
	if len(dialed) != 2 || dialed[0] != "example.test:443" || dialed[1] != "lazy.test:443" {
		t.Fatalf("dialed = %v, want [example.test:443 lazy.test:443]", dialed)
	}
}

func TestNoDirectDialOutsideConnectionGo(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "connection.go" {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == "dial" || ident.Name == "dialLazy" {
				pos := fset.Position(call.Pos())
				t.Errorf("%s:%d calls %s directly; use Runner.withSession / Runner.dialServer / Runner.dialServerLazy", name, pos.Line, ident.Name)
			}
			return true
		})
	}
}
