package cli_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/proto/core/v1/corev1connect"
)

func TestConnectHTTPLoginAndListSlices(t *testing.T) {
	ts := startTestServer(t)
	slicesClient := corev1connect.NewSliceServiceClient(http.DefaultClient, connectBaseURL(ts.httpAddr))

	// An account that does not exist is not found, token or not.
	_, err := slicesClient.ListSlices(context.Background(), connect.NewRequest(&corev1.ListSlicesRequest{
		Account: "no-such-account",
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected ListSlices of an unknown account to be not found, got %v", err)
	}
	_, err = slicesClient.ListSlices(context.Background(), connectRequest("not-a-token", &corev1.ListSlicesRequest{
		Account: "acme",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected invalid-token ListSlices to return unauthenticated, got %v", err)
	}
	statusCode, headers := httpAPIOptions(t, ts.httpAddr, corev1connect.SliceServiceListSlicesProcedure, "http://web.test")
	if statusCode != http.StatusNoContent {
		t.Fatalf("expected CORS preflight to return 204, got %d", statusCode)
	}
	if got := headers.Get("Access-Control-Allow-Origin"); got != "http://web.test" {
		t.Fatalf("expected CORS allow-origin for web app, got %q", got)
	}

	token, subjectID := ts.defaultAcmeCredentials(t)
	if subjectID == "" {
		t.Fatal("expected service-token provisioning to return subject id")
	}

	response, err := slicesClient.ListSlices(context.Background(), connectRequest(token, &corev1.ListSlicesRequest{
		Account: "acme",
	}))
	if err != nil {
		t.Fatalf("ListSlices: %v", err)
	}
	for _, slice := range response.Msg.Slices {
		if slice.GetRef().GetAccount() == "acme" && slice.GetRef().GetSlice() == "payment" {
			return
		}
	}
	t.Fatalf("expected acme:payment slice in response: %#v", response.Msg.Slices)
}

func TestConnectHTTPWriteChangesetFlow(t *testing.T) {
	ts := startTestServer(t)
	token, _ := ts.defaultAcmeCredentials(t)

	blobClient := corev1connect.NewBlobServiceClient(http.DefaultClient, connectBaseURL(ts.httpAddr))
	changesetClient := corev1connect.NewChangesetServiceClient(http.DefaultClient, connectBaseURL(ts.httpAddr))
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}

	content := []byte("package payment\nconst GatewayWrite = true\n")
	upload, err := blobClient.UploadBlob(context.Background(), connectRequest(token, &corev1.UploadBlobRequest{
		Data:  content,
		Slice: sliceRef,
	}))
	if err != nil {
		t.Fatalf("UploadBlob: %v", err)
	}
	blobID := upload.Msg.GetBlobId()
	contentHash := upload.Msg.GetContentHash()
	if blobID == "" || contentHash == "" {
		t.Fatalf("expected uploaded blob id and hash: %#v", upload.Msg)
	}

	blobStatus, err := blobClient.GetBlobStatus(context.Background(), connectRequest(token, &corev1.GetBlobStatusRequest{
		ContentHashes: []string{contentHash, "sha256:missing"},
		Slice:         sliceRef,
	}))
	if err != nil {
		t.Fatalf("GetBlobStatus: %v", err)
	}
	records := blobStatus.Msg.GetBlobs()
	if len(records) != 2 {
		t.Fatalf("expected two blob status records: %#v", blobStatus.Msg)
	}
	if records[0].GetState() != "available" {
		t.Fatalf("expected uploaded blob to be available: %#v", blobStatus.Msg)
	}
	if records[1].GetState() != "missing" {
		t.Fatalf("expected unknown blob to be missing: %#v", blobStatus.Msg)
	}

	cs, err := changesetClient.CreateChangeset(context.Background(), connectRequest(token, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		Title:          "Connect write",
		Description:    "created through Connect HTTP",
	}))
	if err != nil {
		t.Fatalf("CreateChangeset: %v", err)
	}
	changesetID := cs.Msg.GetId()
	baseCommitID := cs.Msg.GetBaseCommitId()
	if changesetID == "" || baseCommitID == "" {
		t.Fatalf("expected changeset id and base commit: %#v", cs.Msg)
	}

	patchset, err := changesetClient.UpdateChangeset(context.Background(), connectRequest(token, &corev1.UpdateChangesetRequest{
		ChangesetId:  changesetID,
		BaseCommitId: baseCommitID,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/connect_write.go",
			BlobId:      blobID,
			ContentHash: contentHash,
			Mode:        0o644,
		}},
	}))
	if err != nil {
		t.Fatalf("UpdateChangeset: %v", err)
	}
	patchsetID := patchset.Msg.GetId()
	if patchsetID == "" {
		t.Fatalf("expected patchset id: %#v", patchset.Msg)
	}

	submit, err := changesetClient.SubmitChangeset(context.Background(), connectRequest(token, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	}))
	if err != nil {
		t.Fatalf("SubmitChangeset: %v", err)
	}
	if status := submit.Msg.GetStatus(); status != "pending_publish" && status != "submitted" {
		t.Fatalf("unexpected submit response: %#v", submit.Msg)
	}
	published := waitForConnectChangesetStatus(t, changesetClient, token, changesetID, "submitted")
	if commitID := published.GetCommitId(); commitID == "" {
		t.Fatalf("expected published commit id: %#v", published)
	}
}
