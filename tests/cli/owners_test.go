package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPathOwnersGroupsAndGitPushEndToEnd(t *testing.T) {
	ts := startTestServer(t)

	ownerHome := t.TempDir()
	ownerWS := t.TempDir()
	loginTestCLI(t, ts, ownerHome, ownerWS)
	ownerToken := readToken(t, ownerHome)

	reviewerToken, reviewerSubject, _ := ts.provisionAccount(t, "rev-owner", "rev-owner")
	reviewerHome := t.TempDir()
	writeCLIAuthConfig(t, reviewerHome, ts.addr, reviewerToken, reviewerSubject)

	// Create @acme/api-owners via CLI, add rev-owner, and grant writer slice access via group.
	runCLI(t, ownerHome, ownerWS, "group", "create", "@acme/api-owners")
	runCLI(t, ownerHome, ownerWS, "group", "add", "@acme/api-owners", "rev-owner")
	runCLI(t, ownerHome, ownerWS, "slice", "share", "acme:payment", "@acme/api-owners", "--role", "writer")

	membersOut := runCLI(t, ownerHome, ownerWS, "slice", "members", "acme:payment")
	if !strings.Contains(membersOut, "@acme/api-owners") {
		t.Fatalf("slice members missing @acme/api-owners:\n%s", membersOut)
	}

	// Land .gitslice/owners.yaml on acme/payment.
	runCLI(t, ownerHome, ownerWS, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, ownerWS, "acme/payment/.gitslice/owners.yaml",
		"version: 1\nowners:\n  - paths: [\"api/**\"]\n    owners: [\"@acme/api-owners\"]\n    require: 1\n")
	writeWorkspaceFile(t, ownerWS, "acme/payment/api/endpoint.go", "package api\nconst V = 1\n")
	runCLI(t, ownerHome, ownerWS, "create", "--all", "--message", "add owners.yaml", "--submit")

	// Clone via Git HTTP and verify direct main push is blocked.
	cloneDir := filepath.Join(t.TempDir(), "payment-git")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+ownerToken, "clone", gitURL, cloneDir)
	runGit(t, "", "-C", cloneDir, "config", "user.name", "Owner")
	runGit(t, "", "-C", cloneDir, "config", "user.email", "owner@example.invalid")

	writeWorkspaceFile(t, cloneDir, "acme/payment/api/endpoint.go", "package api\nconst V = 2\n")
	runGit(t, "", "-C", cloneDir, "add", "acme/payment/api/endpoint.go")
	runGit(t, "", "-C", cloneDir, "commit", "-m", "update api via git push")

	if _, stderr, err := runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+ownerToken, "push", "origin", "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("expected direct push to refs/heads/main to be rejected; stderr:\n%s", stderr)
	}

	_, pushStderr, err := runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+ownerToken, "push", "origin", "HEAD:refs/changes/new")
	if err != nil {
		t.Fatalf("git push refs/changes/new failed: %v\nstderr:\n%s", err, pushStderr)
	}
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	changesets := newTestCoreClients(conn).changeset
	draft := singleDraftChangeset(t, grpcAuthContext(ownerToken), changesets)
	csSelector := shortChangesetID(draft.Id)

	// Non-owner submitting the pushed changeset is blocked on path owner approval (and stores SubmitBlockedReason).
	_, subErr := runCLIFails(t, ownerHome, ownerWS, "cs", "submit", csSelector)
	if !strings.Contains(subErr, "path owner approvals not satisfied") {
		t.Fatalf("expected path owner block, got:\n%s", subErr)
	}
	statusOut := runCLI(t, ownerHome, ownerWS, "cs", "status", csSelector)
	if !strings.Contains(statusOut, "path owner approvals not satisfied") {
		t.Fatalf("cs status missing path owner block reason:\n%s", statusOut)
	}

	// Member of @acme/api-owners approves -> clears SubmitBlockedReason and allows submit.
	runCLI(t, reviewerHome, t.TempDir(), "cs", "approve", csSelector)
	clearedStatus := runCLI(t, ownerHome, ownerWS, "cs", "status", csSelector)
	if strings.Contains(clearedStatus, "path owner approvals not satisfied") {
		t.Fatalf("expected block reason cleared after approval:\n%s", clearedStatus)
	}
	runCLI(t, ownerHome, ownerWS, "submit", csSelector)
	waitForSubmittedChangeset(t, grpcAuthContext(ownerToken), changesets, draft.Id)

	// Invalid .gitslice/owners.yaml pushed over Git HTTP is rejected cleanly.
	writeWorkspaceFile(t, cloneDir, "acme/payment/.gitslice/owners.yaml", "version: 99\nowners: []\n")
	runGit(t, "", "-C", cloneDir, "add", "acme/payment/.gitslice/owners.yaml")
	runGit(t, "", "-C", cloneDir, "commit", "-m", "invalid owners.yaml")
	if _, stderr, err := runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+ownerToken, "push", "origin", "HEAD:refs/changes/new"); err == nil {
		t.Fatalf("expected invalid owners.yaml git push to be rejected; stderr:\n%s", stderr)
	}
}
