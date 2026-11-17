package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func assertServiceCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want gRPC code %s", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("gRPC code = %s (%v), want %s", got, err, want)
	}
}

func TestCreateDelegatedAgentServiceAuthorization(t *testing.T) {
	ctx := context.Background()
	mem, handlers := newMemoryHandlers()

	ownerID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, ownerID, "owner"); err != nil {
		t.Fatal(err)
	}
	writerID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_writer", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, writerID, "writer"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.Create(ctx, ownerID, &corev1.SliceRef{Account: "owner", Slice: "app"}, []string{"/owner/app"}, "private", 0, nil); err != nil {
		t.Fatal(err)
	}
	sliceObj, err := mem.Slices.Resolve(ctx, &corev1.SliceRef{Account: "owner", Slice: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.SetMember(ctx, sliceObj.Id, writerID, "writer", ownerID); err != nil {
		t.Fatal(err)
	}

	ownerCtx := authctx.WithSubjectID(ctx, ownerID)
	writerCtx := authctx.WithSubjectID(ctx, writerID)

	// Writer on owner/app cannot create a delegated agent on owner/app (needs ManageMembers),
	// and failure must not leave an orphaned agent account behind.
	_, err = handlers.Auth.CreateDelegatedAgent(writerCtx, &corev1.CreateDelegatedAgentRequest{
		Username: "denied-bot",
		Slice:    &corev1.SliceRef{Account: "owner", Slice: "app"},
		Role:     "contributor",
	})
	assertServiceCode(t, err, codes.PermissionDenied)
	if _, lookupErr := mem.Auth.GetAccountProfile(ctx, "denied-bot"); !errors.Is(lookupErr, storage.ErrNotFound) {
		t.Fatalf("expected denied-bot not to exist, got err=%v", lookupErr)
	}

	// Owner acting through a slice-scoped or role-capped API key cannot mint a delegated agent.
	_, scopedSecret, _, _, err := mem.Auth.CreateAPIKey(ctx, storage.CreateAPIKeyInput{
		ActorID:  ownerID,
		Name:     "scoped",
		SliceIDs: []string{sliceObj.Id},
		MaxRole:  "writer",
	})
	if err != nil {
		t.Fatal(err)
	}
	scopedPrincipal, err := mem.Auth.AuthenticateToken(ctx, scopedSecret)
	if err != nil {
		t.Fatal(err)
	}
	scopedCtx := authctx.WithPrincipal(ctx, *scopedPrincipal)
	_, err = handlers.Auth.CreateDelegatedAgent(scopedCtx, &corev1.CreateDelegatedAgentRequest{
		Username: "blocked-scoped-bot",
		Slice:    &corev1.SliceRef{Account: "owner", Slice: "app"},
	})
	assertServiceCode(t, err, codes.PermissionDenied)

	// Unrestricted human owner creates delegated agent as reader on owner/app.
	resp, err := handlers.Auth.CreateDelegatedAgent(ownerCtx, &corev1.CreateDelegatedAgentRequest{
		Username:    "ci-reader-bot",
		DisplayName: "CI Reader",
		Slice:       &corev1.SliceRef{Account: "owner", Slice: "app"},
		Role:        "reader",
	})
	if err != nil {
		t.Fatalf("CreateDelegatedAgent: %v", err)
	}
	if resp.Account != "ci-reader-bot" || resp.Role != "reader" || !strings.HasPrefix(resp.Secret, storage.APIKeyPrefix) {
		t.Fatalf("unexpected CreateDelegatedAgent response: %+v", resp)
	}

	// Delegated agent itself cannot create another delegated agent.
	botPrincipal, err := mem.Auth.AuthenticateToken(ctx, resp.Secret)
	if err != nil {
		t.Fatal(err)
	}
	botCtx := authctx.WithPrincipal(ctx, *botPrincipal)
	_, err = handlers.Auth.CreateDelegatedAgent(botCtx, &corev1.CreateDelegatedAgentRequest{
		Username: "nested-bot",
	})
	assertServiceCode(t, err, codes.PermissionDenied)

	// Delegated agent added as reader on owner/app can resolve the slice but cannot create changesets.
	if _, err := handlers.Slice.ResolveSlice(botCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "owner", Slice: "app"},
	}); err != nil {
		t.Fatalf("reader delegated agent ResolveSlice: %v", err)
	}
	_, err = handlers.Changeset.CreateChangeset(botCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "owner", Slice: "app"},
		Title:          "denied propose",
	})
	assertServiceCode(t, err, codes.PermissionDenied)
}

func TestConversationRolesAndControlEnforcement(t *testing.T) {
	ctx := context.Background()
	mem, handlers := newMemoryHandlers()

	ownerID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, ownerID, "owner"); err != nil {
		t.Fatal(err)
	}
	readerID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_reader", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, readerID, "reader"); err != nil {
		t.Fatal(err)
	}
	contribID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_contrib", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, contribID, "contrib"); err != nil {
		t.Fatal(err)
	}
	strangerID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_stranger", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, strangerID, "stranger"); err != nil {
		t.Fatal(err)
	}

	pubSlice, err := mem.Slices.Create(ctx, ownerID, &corev1.SliceRef{Account: "owner", Slice: "docs"}, []string{"/owner/docs"}, "public", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.SetMember(ctx, pubSlice.Id, readerID, "reader", ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.SetMember(ctx, pubSlice.Id, contribID, "contributor", ownerID); err != nil {
		t.Fatal(err)
	}

	ownerCtx := authctx.WithSubjectID(ctx, ownerID)
	stream := newFakeAgentConnectStream(ownerCtx)
	errCh := make(chan error, 1)
	go func() { errCh <- handlers.Agent.Connect(stream) }()
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name: "role-daemon", Runtime: "codex",
	}}}
	daemonID := (<-stream.sent).GetRegistered().GetDaemonId()
	defer func() {
		close(stream.recv)
		_ = <-errCh
	}()

	// Owner's daemon is exposed/owned only by owner, wait: make daemon shared or test owner creating convs:
	// First check role validation & reader/contributor gating via owner's daemon vs contrib's daemon.
	contribCtx := authctx.WithSubjectID(ctx, contribID)
	contribStream := newFakeAgentConnectStream(contribCtx)
	cErrCh := make(chan error, 1)
	go func() { cErrCh <- handlers.Agent.Connect(contribStream) }()
	contribStream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name: "contrib-daemon", Runtime: "codex",
	}}}
	contribDaemonID := (<-contribStream.sent).GetRegistered().GetDaemonId()
	defer func() {
		close(contribStream.recv)
		_ = <-cErrCh
	}()

	// Contributor cannot create a writer conversation (exceeds contributor role).
	_, err = handlers.Agent.CreateConversation(contribCtx, &corev1.CreateConversationRequest{
		DaemonId: contribDaemonID,
		Slice:    &corev1.SliceRef{Account: "owner", Slice: "docs"},
		Role:     "writer",
	})
	assertServiceCode(t, err, codes.PermissionDenied)

	// Invalid role rejected.
	_, err = handlers.Agent.CreateConversation(ownerCtx, &corev1.CreateConversationRequest{
		DaemonId: daemonID,
		Slice:    &corev1.SliceRef{Account: "owner", Slice: "docs"},
		Role:     "admin",
	})
	assertServiceCode(t, err, codes.InvalidArgument)

	// Owner creates a reader conversation and verifies StartConversation carries Role=="reader".
	conv, err := handlers.Agent.CreateConversation(ownerCtx, &corev1.CreateConversationRequest{
		DaemonId: daemonID,
		Slice:    &corev1.SliceRef{Account: "owner", Slice: "docs"},
		Title:    "read-only question",
		Role:     "reader",
	})
	if err != nil {
		t.Fatalf("CreateConversation reader: %v", err)
	}
	if conv.GetRole() != "reader" {
		t.Fatalf("conv.Role = %q, want reader", conv.GetRole())
	}
	startMsg := waitForStart(t, stream)
	if startMsg.GetRole() != "reader" {
		t.Fatalf("StartConversation = %#v, want role reader", startMsg)
	}

	// Stranger on the public slice cannot send messages to or close owner's reader conversation.
	strangerCtx := authctx.WithSubjectID(ctx, strangerID)
	_, err = handlers.Agent.SendAgentMessage(strangerCtx, &corev1.SendAgentMessageRequest{
		ConversationId: conv.Id,
		Text:           "hijack",
	})
	assertServiceCode(t, err, codes.PermissionDenied)
	_, err = handlers.Agent.CloseConversation(strangerCtx, &corev1.CloseConversationRequest{
		ConversationId: conv.Id,
	})
	assertServiceCode(t, err, codes.PermissionDenied)

	// Contributor can create and message a contributor conversation on its own daemon,
	// and loses control if downgraded to reader.
	cConv, err := handlers.Agent.CreateConversation(contribCtx, &corev1.CreateConversationRequest{
		DaemonId: contribDaemonID,
		Slice:    &corev1.SliceRef{Account: "owner", Slice: "docs"},
		Title:    "contrib run",
	})
	if err != nil {
		t.Fatalf("contrib CreateConversation: %v", err)
	}
	if cConv.GetRole() != "contributor" {
		t.Fatalf("default conv role = %q, want contributor", cConv.GetRole())
	}
	_ = waitForStart(t, contribStream)
	if _, err := handlers.Agent.SendAgentMessage(contribCtx, &corev1.SendAgentMessageRequest{
		ConversationId: cConv.Id,
		Text:           "ok while contributor",
	}); err != nil {
		t.Fatalf("SendAgentMessage while contributor: %v", err)
	}
	if _, err := mem.Slices.SetMember(ctx, pubSlice.Id, contribID, "reader", ownerID); err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Agent.SendAgentMessage(contribCtx, &corev1.SendAgentMessageRequest{
		ConversationId: cConv.Id,
		Text:           "denied after downgrade",
	})
	assertServiceCode(t, err, codes.PermissionDenied)
	_, err = handlers.Agent.CloseConversation(contribCtx, &corev1.CloseConversationRequest{
		ConversationId: cConv.Id,
	})
	assertServiceCode(t, err, codes.PermissionDenied)

	// Owner can close owner's reader conversation.
	if _, err := handlers.Agent.CloseConversation(ownerCtx, &corev1.CloseConversationRequest{
		ConversationId: conv.Id,
	}); err != nil {
		t.Fatalf("owner CloseConversation: %v", err)
	}
}
