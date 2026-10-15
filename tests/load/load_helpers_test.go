//go:build load

package load_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/cli"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func loadUploadFileEdit(clients loadCoreClients, p string, content []byte) (*corev1.FileEdit, error) {
	upload, err := clients.blob.UploadBlob(clients.ctx, &corev1.UploadBlobRequest{
		ContentHash: objectid.RawContentHash(content),
		Data:        content,
		Slice:       &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		return nil, err
	}
	return &corev1.FileEdit{
		Op:          "upsert",
		Path:        p,
		BlobId:      upload.BlobId,
		ContentHash: upload.ContentHash,
		Mode:        0o100644,
	}, nil
}

func submitLoadEdits(clients loadCoreClients, sliceRef *corev1.SliceRef, title string, edits []*corev1.FileEdit) (string, time.Duration, error) {
	begin := time.Now()
	ref, err := clients.repo.GetRef(clients.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return "", 0, err
	}
	cs, err := clients.changeset.CreateChangeset(clients.ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          title,
	})
	if err != nil {
		return "", 0, err
	}
	patchset, err := clients.changeset.UpdateChangeset(clients.ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    edits,
	})
	if err != nil {
		return "", 0, err
	}
	if _, err := clients.changeset.SubmitChangeset(clients.ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		return "", 0, err
	}
	commitID, err := waitForRPCSubmittedWithTimeout(clients.ctx, clients.changeset, cs.Id, loadPublishTimeout(len(edits)))
	if err != nil {
		return "", 0, err
	}
	return commitID, time.Since(begin), nil
}

func loadPublishTimeout(editCount int) time.Duration {
	timeout := 2 * time.Minute
	if editCount > 0 {
		timeout += time.Duration(editCount/100) * time.Second
	}
	if timeout > 15*time.Minute {
		return 15 * time.Minute
	}
	return timeout
}

func requireEntry(entries []*corev1.TreeEntry, wantPath string, wantKind corev1.EntryKind) error {
	for _, entry := range entries {
		if entry.Path == wantPath && entry.Kind == wantKind {
			return nil
		}
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, fmt.Sprintf("%s:%s", entry.Path, entry.Kind))
	}
	sort.Strings(got)
	return fmt.Errorf("missing %s:%s from [%s]", wantPath, wantKind, strings.Join(got, ", "))
}

func runCLI(t *testing.T, home, workspace string, args ...string) string {
	t.Helper()
	out, err := runCLIResult(home, workspace, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func runCLIResult(home, workspace string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err != nil {
		return stdout.String(), fmt.Errorf("gs %s failed: %w\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), nil
}

func writeWorkspaceFile(t *testing.T, workspace, rel, content string) {
	t.Helper()
	path := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
