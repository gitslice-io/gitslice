package rpc_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func createImportGitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Import Tester")
	runGit(t, repo, "config", "user.email", "importer@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "first commit")
	if err := os.MkdirAll(filepath.Join(repo, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "lib", "code.go"), []byte("package lib\nconst Value = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "lib/code.go")
	runGit(t, repo, "commit", "-m", "second commit")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "third commit")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func resolveTestSlice(t *testing.T, ctx context.Context, slices corev1.SliceServiceClient, name string) *corev1.Slice {
	t.Helper()
	slice, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: name},
	})
	if err != nil {
		t.Fatal(err)
	}
	return slice
}

func submitRPCFile(t *testing.T, ctx context.Context, clients testCoreClients, sliceName, path, content string) string {
	t.Helper()
	return submitRPCFileInSlice(t, ctx, clients, "acme", sliceName, path, content)
}

func submitRPCFileInSlice(t *testing.T, ctx context.Context, clients testCoreClients, account, sliceName, path, content string) string {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: &corev1.SliceRef{Account: account, Slice: sliceName}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: account, Slice: sliceName},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "seed " + path,
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	return waitForSubmittedChangeset(t, ctx, clients.changeset, cs.Id).CommitId
}

func assertSliceRef(t *testing.T, slice *corev1.Slice, account, name string) {
	t.Helper()
	if slice == nil || slice.Ref == nil || slice.Ref.Account != account || slice.Ref.Slice != name {
		t.Fatalf("unexpected slice ref: %#v", slice)
	}
	if slice.Id == "" || slice.DefinitionHash == "" || slice.Definition == nil {
		t.Fatalf("slice missing identity or definition: %#v", slice)
	}
}

func assertStringSet(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("strings = %#v, want %#v", got, want)
	}
	counts := map[string]int{}
	for _, value := range got {
		counts[value]++
	}
	for _, value := range want {
		counts[value]--
	}
	for value, count := range counts {
		if count != 0 {
			t.Fatalf("strings = %#v, want %#v; mismatch at %q count %d", got, want, value, count)
		}
	}
}

func assertEntryNameSet(t *testing.T, entries []*corev1.TreeEntry, want ...string) {
	t.Helper()
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry != nil {
			names = append(names, entry.Name)
		}
	}
	assertStringSet(t, names, want...)
}

func assertPathCoverage(t *testing.T, coverage []*corev1.PathCoverage, path string, wantIDs ...string) {
	t.Helper()
	for _, entry := range coverage {
		if entry != nil && entry.Path == path {
			assertStringSet(t, entry.CoveringSliceIds, wantIDs...)
			return
		}
	}
	t.Fatalf("coverage for %s not found in %#v", path, coverage)
}

func assertCommitSetIncludes(t *testing.T, commits []*corev1.Commit, wantIDs ...string) {
	t.Helper()
	got := commitIDSet(commits)
	for _, want := range wantIDs {
		if !got[want] {
			t.Fatalf("commit history missing %s; got %#v", want, commitIDs(commits))
		}
	}
}

func assertCommitSetExcludes(t *testing.T, commits []*corev1.Commit, rejectedIDs ...string) {
	t.Helper()
	got := commitIDSet(commits)
	for _, rejected := range rejectedIDs {
		if got[rejected] {
			t.Fatalf("commit history included %s; got %#v", rejected, commitIDs(commits))
		}
	}
}

func assertCommitIDs(t *testing.T, commits []*corev1.Commit, wantIDs ...string) {
	t.Helper()
	got := commitIDs(commits)
	if len(got) != len(wantIDs) {
		t.Fatalf("commit ids = %#v, want %#v", got, wantIDs)
	}
	for i, want := range wantIDs {
		if got[i] != want {
			t.Fatalf("commit ids = %#v, want %#v", got, wantIDs)
		}
	}
}

func commitIDSet(commits []*corev1.Commit) map[string]bool {
	out := make(map[string]bool, len(commits))
	for _, commit := range commits {
		if commit != nil {
			out[commit.Id] = true
		}
	}
	return out
}

func commitIDs(commits []*corev1.Commit) []string {
	out := make([]string, 0, len(commits))
	for _, commit := range commits {
		if commit != nil {
			out = append(out, commit.Id)
		}
	}
	return out
}
