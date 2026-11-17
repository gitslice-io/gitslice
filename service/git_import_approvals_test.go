package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// gitsliceConfigRepo makes a repository whose only commit adds a .gitslice
// configuration file, which decides what runs with the account's secrets.
func gitsliceConfigRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-q")
	runGitTest(t, repo, "config", "user.name", "Importer")
	runGitTest(t, repo, "config", "user.email", "importer@example.invalid")
	p := filepath.Join(repo, ".gitslice", "checks.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("version: 1\nchecks: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "-A")
	runGitTest(t, repo, "commit", "-q", "-m", "add checks")
	return repo
}

// An import goes through the submit rules that live outside the store: a
// writer who isn't an owner or admin of the account can't land a .gitslice
// change by importing it, and an admin can.
func TestImportNeedsGitsliceApprovalLikeSubmit(t *testing.T) {
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "1")
	_, handlers := newSliceMemberHandlers(t)
	if _, err := handlers.Slice.SetSliceMember(subjectCtx("user_alice"), &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "bob",
		Role:     "writer",
	}); err != nil {
		t.Fatalf("SetSliceMember(writer): %v", err)
	}
	repo := gitsliceConfigRepo(t)
	req := func() *corev1.ImportGitRepositoryRequest {
		return &corev1.ImportGitRepositoryRequest{
			Source:         repo,
			MountPath:      "/acme/payment/imported",
			AuthoringSlice: paymentRef,
			Mode:           "deep",
		}
	}

	bob := authctx.WithSubjectID(context.Background(), "user_bob")
	_, err := handlers.Repository.importGitRepository(bob, req(), nil)
	if err == nil {
		t.Fatal("writer imported a .gitslice change without an owner or admin")
	}
	if !strings.Contains(err.Error(), "import stopped") || !strings.Contains(err.Error(), ".gitslice") {
		t.Fatalf("error = %v, want the import stopped for .gitslice approval", err)
	}
	if code := status.Code(err); code != codes.FailedPrecondition && code != codes.Aborted {
		t.Fatalf("code = %v, want FailedPrecondition", code)
	}

	alice := authctx.WithSubjectID(context.Background(), "user_alice")
	res, err := handlers.Repository.importGitRepository(alice, req(), nil)
	if err != nil {
		t.Fatalf("admin import: %v", err)
	}
	if len(res.Commits) != 1 {
		t.Fatalf("imported %d commits, want 1", len(res.Commits))
	}
}
