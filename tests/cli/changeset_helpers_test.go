package cli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/proto/core/v1/corev1connect"
)

func createDirectPatchset(t *testing.T, ctx context.Context, clients testCoreClients, path, content, title string) (string, string) {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          title,
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs.Id, patchset.Id
}

func waitForSubmittedChangeset(t *testing.T, ctx context.Context, client corev1.ChangesetServiceClient, changesetID string) *corev1.Changeset {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last *corev1.Changeset
	for time.Now().Before(deadline) {
		cs, err := client.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
		if err != nil {
			t.Fatal(err)
		}
		last = cs
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("changeset %s did not reach submitted status, last=%#v", changesetID, last)
	return nil
}

func singleDraftChangeset(t *testing.T, ctx context.Context, client corev1.ChangesetServiceClient) *corev1.Changeset {
	t.Helper()
	res, err := client.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Status:         "draft",
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changesets) != 1 {
		t.Fatalf("draft changeset count = %d, want 1: %#v", len(res.Changesets), res.Changesets)
	}
	return res.Changesets[0]
}

func submittedRefCommitID(t *testing.T, raw string) string {
	t.Helper()
	var res struct {
		NewRefCommitID string `json:"new_ref_commit_id"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("submit output is not JSON: %v\n%s", err, raw)
	}
	if res.NewRefCommitID == "" {
		t.Fatalf("submit output missing new_ref_commit_id:\n%s", raw)
	}
	return res.NewRefCommitID
}

func parseChangesetOutput(t *testing.T, raw string) (string, string, string, int64) {
	t.Helper()
	var res struct {
		ChangesetID    string `json:"changeset_id"`
		PatchsetID     string `json:"patchset_id"`
		PatchsetNumber int64  `json:"patchset_number"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("changeset output is not JSON: %v\n%s", err, raw)
	}
	selector := shortChangesetID(res.ChangesetID)
	if res.ChangesetID == "" || res.PatchsetID == "" || selector == "" || res.PatchsetNumber == 0 {
		t.Fatalf("changeset output missing ids:\n%s", raw)
	}
	if !isLowerHex(selector) {
		t.Fatalf("changeset selector is not lower hex: %q", selector)
	}
	return res.ChangesetID, res.PatchsetID, selector, res.PatchsetNumber
}

func shortChangesetID(id string) string {
	body := strings.TrimPrefix(id, "cs_")
	if len(body) > 10 {
		return body[:10]
	}
	return body
}

func isLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func waitForConnectChangesetStatus(t *testing.T, client corev1connect.ChangesetServiceClient, token, changesetID, want string) *corev1.Changeset {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last *corev1.Changeset
	for time.Now().Before(deadline) {
		response, err := client.GetChangeset(context.Background(), connectRequest(token, &corev1.GetChangesetRequest{
			ChangesetId: changesetID,
		}))
		if err != nil {
			t.Fatalf("GetChangeset: %v", err)
		}
		last = response.Msg
		if status := last.GetStatus(); status == want {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("changeset %s did not reach %s status, last=%#v", changesetID, want, last)
	return nil
}
