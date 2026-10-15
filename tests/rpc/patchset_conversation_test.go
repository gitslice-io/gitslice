package rpc_test

import (
	"context"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestPatchsetConversationLink verifies that a patchset created with a
// conversation id records the conversation and the correct event-seq cutoff, and
// that GetConversationEvents returns the exchange that produced each patchset.
func TestPatchsetConversationLink(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	agent := corev1.NewAgentServiceClient(conn)
	clients := newTestCoreClients(conn)

	// Register a daemon so a conversation can be created.
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
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Title:    "link test",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	// Two messages -> seq 1 and 2 -> these produce the first patchset.
	for _, text := range []string{"do the first change", "and refine it"} {
		if _, err := agent.SendAgentMessage(ctx, &corev1.SendAgentMessageRequest{ConversationId: conv.Id, Text: text}); err != nil {
			t.Fatalf("SendAgentMessage: %v", err)
		}
	}

	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		BaseCommitId:   ref.CommitId,
		Title:          "agent change",
	})
	if err != nil {
		t.Fatalf("CreateChangeset: %v", err)
	}

	up1 := uploadPaymentFile(t, ctx, clients, "/acme/payment/agent-link/a.txt", "v1\n")
	ps1, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:    cs.Id,
		BaseCommitId:   ref.CommitId,
		FileEdits:      []*corev1.FileEdit{up1},
		ConversationId: conv.Id,
	})
	if err != nil {
		t.Fatalf("UpdateChangeset 1: %v", err)
	}
	if ps1.AuthoringConversationId != conv.Id || ps1.AuthoringConversationSeq != 2 {
		t.Fatalf("patchset 1 conversation = (%q, %d), want (%q, 2)", ps1.AuthoringConversationId, ps1.AuthoringConversationSeq, conv.Id)
	}

	// A third message -> seq 3 -> produces the second patchset.
	if _, err := agent.SendAgentMessage(ctx, &corev1.SendAgentMessageRequest{ConversationId: conv.Id, Text: "one more tweak"}); err != nil {
		t.Fatalf("SendAgentMessage 3: %v", err)
	}
	up2 := uploadPaymentFile(t, ctx, clients, "/acme/payment/agent-link/b.txt", "v2\n")
	ps2, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: ps1.Id,
		BaseCommitId:              ref.CommitId,
		FileEdits:                 []*corev1.FileEdit{up2},
		ConversationId:            conv.Id,
	})
	if err != nil {
		t.Fatalf("UpdateChangeset 2: %v", err)
	}
	if ps2.AuthoringConversationSeq != 3 {
		t.Fatalf("patchset 2 seq = %d, want 3", ps2.AuthoringConversationSeq)
	}

	// The first patchset's exchange is events (0, 2]; the second's is (2, 3].
	first, err := agent.GetConversationEvents(ctx, &corev1.GetConversationEventsRequest{ConversationId: conv.Id, AfterSeq: 0, BeforeSeq: ps1.AuthoringConversationSeq})
	if err != nil {
		t.Fatalf("GetConversationEvents 1: %v", err)
	}
	if len(first.Events) != 2 {
		t.Fatalf("patchset 1 events = %d, want 2", len(first.Events))
	}
	second, err := agent.GetConversationEvents(ctx, &corev1.GetConversationEventsRequest{ConversationId: conv.Id, AfterSeq: ps1.AuthoringConversationSeq, BeforeSeq: ps2.AuthoringConversationSeq})
	if err != nil {
		t.Fatalf("GetConversationEvents 2: %v", err)
	}
	if len(second.Events) != 1 || second.Events[0].Text != "one more tweak" {
		t.Fatalf("patchset 2 events = %#v, want [one more tweak]", second.Events)
	}

	cancelDaemon()
}
