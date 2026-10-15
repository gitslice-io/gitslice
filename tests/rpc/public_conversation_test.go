package rpc_test

import (
	"context"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

// TestPublicSliceConversationReadableWriteGated verifies that a conversation on
// a public slice follows the slice's visibility for reads (anyone may load it,
// matching how the changeset detail page fetches the exchange behind a patchset)
// while talking to it stays write-gated to the slice's members.
func TestPublicSliceConversationReadableWriteGated(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	agent := corev1.NewAgentServiceClient(conn)
	slices := corev1.NewSliceServiceClient(conn)

	// Register a daemon, open a conversation on acme/payment, and seed an event.
	daemonCtx, cancelDaemon := context.WithCancel(ctx)
	defer cancelDaemon()
	stream, err := agent.Connect(daemonCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{Name: "d", Runtime: "echo"}}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	reg, err := stream.Recv()
	if err != nil {
		t.Fatalf("registered: %v", err)
	}
	daemonID := reg.GetRegistered().GetDaemonId()

	conv, err := agent.CreateConversation(ctx, &corev1.CreateConversationRequest{
		DaemonId: daemonID,
		Slice:    testPaymentSliceRef(),
		Title:    "public read test",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := agent.SendAgentMessage(ctx, &corev1.SendAgentMessageRequest{ConversationId: conv.Id, Text: "hello"}); err != nil {
		t.Fatalf("SendAgentMessage: %v", err)
	}

	// Before publishing, the slice is private: an anonymous caller cannot read.
	anonCtx := context.Background()
	_, err = agent.GetConversation(anonCtx, &corev1.GetConversationRequest{ConversationId: conv.Id})
	assertGRPCCode(t, err, codes.Unauthenticated)

	// Publish the slice.
	payment, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: testPaymentSliceRef()})
	if err != nil {
		t.Fatalf("ResolveSlice: %v", err)
	}
	if _, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                payment.Id,
		ExpectedDefinitionHash: payment.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: payment.Definition.IncludedPaths,
			Visibility:    "public",
		},
	}); err != nil {
		t.Fatalf("UpdateSliceDefinition public: %v", err)
	}

	// Anonymous reads now succeed: GetConversation, GetConversationEvents, and
	// the StreamConversation replay all return the persisted exchange.
	if _, err := agent.GetConversation(anonCtx, &corev1.GetConversationRequest{ConversationId: conv.Id}); err != nil {
		t.Fatalf("anonymous GetConversation on public slice: %v", err)
	}
	gotEvents, err := agent.GetConversationEvents(anonCtx, &corev1.GetConversationEventsRequest{ConversationId: conv.Id})
	if err != nil {
		t.Fatalf("anonymous GetConversationEvents on public slice: %v", err)
	}
	if len(gotEvents.Events) != 1 || gotEvents.Events[0].Text != "hello" {
		t.Fatalf("anonymous events = %#v, want [hello]", gotEvents.Events)
	}
	replayCtx, cancelReplay := context.WithTimeout(anonCtx, 10*time.Second)
	defer cancelReplay()
	replay, err := agent.StreamConversation(replayCtx, &corev1.StreamConversationRequest{ConversationId: conv.Id})
	if err != nil {
		t.Fatalf("anonymous StreamConversation on public slice: %v", err)
	}
	first, err := replay.Recv()
	if err != nil {
		t.Fatalf("anonymous stream recv: %v", err)
	}
	if first.Seq != 1 || first.Text != "hello" {
		t.Fatalf("anonymous stream first = %#v, want seq 1 hello", first)
	}
	cancelReplay()

	// But only members may talk: an anonymous caller is unauthenticated, and a
	// non-member with a valid subject is denied.
	_, err = agent.SendAgentMessage(anonCtx, &corev1.SendAgentMessageRequest{ConversationId: conv.Id, Text: "intruder"})
	assertGRPCCode(t, err, codes.Unauthenticated)

	outsiderToken, _, _ := ts.provisionAccount(t, "conv-outsider", "conv-outsider")
	outsiderCtx := grpcAuthContext(outsiderToken)
	if _, err := agent.GetConversation(outsiderCtx, &corev1.GetConversationRequest{ConversationId: conv.Id}); err != nil {
		t.Fatalf("outsider read on public slice: %v", err)
	}
	_, err = agent.SendAgentMessage(outsiderCtx, &corev1.SendAgentMessageRequest{ConversationId: conv.Id, Text: "intruder"})
	assertGRPCCode(t, err, codes.PermissionDenied)

	cancelDaemon()
}

func uploadPaymentFile(t *testing.T, ctx context.Context, clients testCoreClients, path, content string) *corev1.FileEdit {
	t.Helper()
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.FileEdit{Op: "add", Path: path, BlobId: upload.BlobId, ContentHash: upload.ContentHash, Mode: 0o644}
}
