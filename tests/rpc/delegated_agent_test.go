package rpc_test

import (
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func TestRPCDelegatedAgentAndConversationRolesWithPostgres(t *testing.T) {
	ts := startRPCServer(t)
	aliceToken := ts.loginViaGRPC(t, "alice")
	bobToken := ts.loginViaGRPC(t, "bobby")
	outsiderToken := ts.loginViaGRPC(t, "outsider")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()

	aliceCtx := grpcAuthContext(aliceToken)
	bobCtx := grpcAuthContext(bobToken)
	outsiderCtx := grpcAuthContext(outsiderToken)
	auth := corev1.NewAuthServiceClient(conn)
	slices := corev1.NewSliceServiceClient(conn)
	agents := corev1.NewAgentServiceClient(conn)
	clients := newTestCoreClients(conn)

	// Non-owner bob cannot create a delegated agent on acme/payment.
	if _, err := auth.CreateDelegatedAgent(bobCtx, &corev1.CreateDelegatedAgentRequest{
		Username: "bob-sneak-bot",
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Role:     "contributor",
	}); err == nil {
		t.Fatal("expected non-owner CreateDelegatedAgent to fail")
	} else {
		assertGRPCCode(t, err, codes.PermissionDenied)
	}

	// Alice creates a delegated reader agent on acme/payment.
	created, err := auth.CreateDelegatedAgent(aliceCtx, &corev1.CreateDelegatedAgentRequest{
		Username:    "payment-auditor",
		DisplayName: "Payment Auditor",
		Slice:       &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Role:        "reader",
	})
	if err != nil {
		t.Fatalf("CreateDelegatedAgent: %v", err)
	}
	if created.Account != "payment-auditor" || created.Role != "reader" || !strings.HasPrefix(created.Secret, storage.APIKeyPrefix) {
		t.Fatalf("unexpected delegated agent response: %+v", created)
	}

	botCtx := grpcAuthContext(created.Secret)
	// Delegated agent can resolve acme/payment as reader, cannot access acme/backend or propose changes.
	if _, err := slices.ResolveSlice(botCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	}); err != nil {
		t.Fatalf("delegated agent ResolveSlice payment: %v", err)
	}
	if _, err := slices.ResolveSlice(botCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "backend"},
	}); err == nil {
		t.Fatal("expected delegated agent ResolveSlice backend to fail")
	} else {
		assertGRPCCode(t, err, codes.PermissionDenied)
	}
	if _, err := clients.changeset.CreateChangeset(botCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Title:          "denied reader propose",
	}); err == nil {
		t.Fatal("expected reader delegated agent CreateChangeset to fail")
	} else {
		assertGRPCCode(t, err, codes.PermissionDenied)
	}

	// Conversation roles persist across Postgres RPCs and enforce slice permissions.
	stream, err := agents.Connect(aliceCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name: "rpc-role-daemon", Runtime: "codex",
	}}}); err != nil {
		t.Fatal(err)
	}
	regMsg, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	daemonID := regMsg.GetRegistered().GetDaemonId()
	defer func() { _ = stream.CloseSend() }()

	conv, err := agents.CreateConversation(aliceCtx, &corev1.CreateConversationRequest{
		DaemonId: daemonID,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Title:    "audit payment",
		Role:     "reader",
	})
	if err != nil {
		t.Fatalf("CreateConversation reader: %v", err)
	}
	if conv.GetRole() != "reader" {
		t.Fatalf("conv.Role = %q, want reader", conv.GetRole())
	}
	var startPush *corev1.StartConversation
	for i := 0; i < 8; i++ {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if startPush = msg.GetStart(); startPush != nil {
			break
		}
	}
	if startPush == nil || startPush.GetRole() != "reader" {
		t.Fatalf("StartConversation = %#v, want role reader", startPush)
	}

	if _, err := agents.SendAgentMessage(outsiderCtx, &corev1.SendAgentMessageRequest{
		ConversationId: conv.Id,
		Text:           "unauthorized",
	}); err == nil {
		t.Fatal("expected outsider SendAgentMessage to fail")
	} else {
		assertGRPCCode(t, err, codes.PermissionDenied)
	}
	if _, err := agents.CloseConversation(outsiderCtx, &corev1.CloseConversationRequest{
		ConversationId: conv.Id,
	}); err == nil {
		t.Fatal("expected outsider CloseConversation to fail")
	} else {
		assertGRPCCode(t, err, codes.PermissionDenied)
	}
	if _, err := agents.CloseConversation(aliceCtx, &corev1.CloseConversationRequest{
		ConversationId: conv.Id,
	}); err != nil {
		t.Fatalf("alice CloseConversation: %v", err)
	}
}
