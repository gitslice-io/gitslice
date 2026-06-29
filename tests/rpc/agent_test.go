package rpc_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

// TestAgentConversationRelay exercises the full bring-your-own-agent relay end
// to end: a daemon registers over Connect, the web side creates a conversation
// and sends a message, the daemon echoes it back as an AgentEvent, and
// StreamConversation surfaces both the user message and the echo. See
// design/16_bring_your_own_agent.md.
func TestAgentConversationRelay(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	agent := corev1.NewAgentServiceClient(conn)

	// Daemon side: open the persistent Connect stream and register.
	daemonCtx, cancelDaemon := context.WithCancel(ctx)
	defer cancelDaemon()
	stream, err := agent.Connect(daemonCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name:    "test-daemon",
		Runtime: "echo",
		Version: "0.0.1",
	}}}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	reg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv registered: %v", err)
	}
	daemonID := reg.GetRegistered().GetDaemonId()
	if daemonID == "" {
		t.Fatalf("expected daemon id, got %#v", reg)
	}

	// Daemon loop: echo every delivered user message back as a final AgentEvent.
	daemonErr := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if errors.Is(err, io.EOF) || daemonCtx.Err() != nil {
				daemonErr <- nil
				return
			}
			if err != nil {
				daemonErr <- err
				return
			}
			um := msg.GetUserMessage()
			if um == nil {
				continue
			}
			if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Event{Event: &corev1.AgentEvent{
				ConversationId: um.ConversationId,
				Role:           "agent",
				Type:           "message",
				Text:           "echo: " + um.Text,
				Final:          true,
			}}}); err != nil {
				daemonErr <- err
				return
			}
		}
	}()

	// The daemon should appear online to the web side.
	daemons, err := agent.ListDaemons(ctx, &corev1.ListDaemonsRequest{})
	if err != nil {
		t.Fatalf("ListDaemons: %v", err)
	}
	if len(daemons.Daemons) != 1 || daemons.Daemons[0].Id != daemonID || daemons.Daemons[0].Status != "online" {
		t.Fatalf("ListDaemons = %#v, want one online daemon %s", daemons.Daemons, daemonID)
	}

	// Web side: open a conversation against the acme/backend slice.
	conv, err := agent.CreateConversation(ctx, &corev1.CreateConversationRequest{
		DaemonId: daemonID,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "backend"},
		Title:    "test conversation",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if conv.Id == "" || conv.DaemonId != daemonID {
		t.Fatalf("unexpected conversation %#v", conv)
	}

	// Open the conversation stream before sending so we observe live events.
	streamCtx, cancelStream := context.WithTimeout(ctx, 10*time.Second)
	defer cancelStream()
	convStream, err := agent.StreamConversation(streamCtx, &corev1.StreamConversationRequest{ConversationId: conv.Id})
	if err != nil {
		t.Fatalf("StreamConversation: %v", err)
	}

	if _, err := agent.SendAgentMessage(ctx, &corev1.SendAgentMessageRequest{ConversationId: conv.Id, Text: "hello"}); err != nil {
		t.Fatalf("SendAgentMessage: %v", err)
	}

	var sawUser, sawEcho bool
	for !sawUser || !sawEcho {
		ev, err := convStream.Recv()
		if err != nil {
			t.Fatalf("stream recv (sawUser=%v sawEcho=%v): %v", sawUser, sawEcho, err)
		}
		switch {
		case ev.Role == "user" && ev.Text == "hello":
			sawUser = true
		case ev.Role == "agent" && ev.Text == "echo: hello":
			sawEcho = true
		}
	}

	// History should be replayable from the persisted log.
	replayCtx, cancelReplay := context.WithCancel(ctx)
	replay, err := agent.StreamConversation(replayCtx, &corev1.StreamConversationRequest{ConversationId: conv.Id})
	if err != nil {
		cancelReplay()
		t.Fatalf("replay StreamConversation: %v", err)
	}
	first, err := replay.Recv()
	if err != nil {
		cancelReplay()
		t.Fatalf("replay recv: %v", err)
	}
	if first.Seq != 1 || first.Role != "user" {
		cancelReplay()
		t.Fatalf("replay first event = %#v, want seq 1 user message", first)
	}
	cancelReplay()

	cancelStream()
	cancelDaemon()
	if err := <-daemonErr; err != nil {
		t.Fatalf("daemon loop error: %v", err)
	}
}

func TestAgentConversationPersistsRuntimeDeltas(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	agent := corev1.NewAgentServiceClient(conn)

	daemonCtx, cancelDaemon := context.WithCancel(ctx)
	defer cancelDaemon()
	stream, err := agent.Connect(daemonCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name:    "delta-daemon",
		Runtime: "test",
	}}}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	reg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv registered: %v", err)
	}
	daemonID := reg.GetRegistered().GetDaemonId()

	conv, err := agent.CreateConversation(ctx, &corev1.CreateConversationRequest{
		DaemonId: daemonID,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "backend"},
		Title:    "delta persistence",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Event{Event: &corev1.AgentEvent{
		ConversationId: conv.Id,
		Role:           "agent",
		Type:           "reasoning_delta",
		Text:           "checking",
		ItemId:         "reason_1",
		Ephemeral:      true,
	}}}); err != nil {
		t.Fatalf("send reasoning delta: %v", err)
	}

	// The daemon->server stream persists events asynchronously, so poll until the
	// delta lands rather than reading once immediately (avoids a flaky race).
	var events *corev1.GetConversationEventsResponse
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, err = agent.GetConversationEvents(ctx, &corev1.GetConversationEventsRequest{ConversationId: conv.Id})
		if err != nil {
			t.Fatalf("GetConversationEvents: %v", err)
		}
		if len(events.Events) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events = %d, want 1 after wait (%#v)", len(events.Events), events.Events)
		}
		time.Sleep(25 * time.Millisecond)
	}
	ev := events.Events[0]
	if ev.Seq != 1 || ev.Type != "reasoning_delta" || ev.Text != "checking" || ev.ItemId != "reason_1" {
		t.Fatalf("persisted delta = %#v, want seq 1 reasoning_delta with item id", ev)
	}

	replayCtx, cancelReplay := context.WithCancel(ctx)
	defer cancelReplay()
	replay, err := agent.StreamConversation(replayCtx, &corev1.StreamConversationRequest{ConversationId: conv.Id})
	if err != nil {
		t.Fatalf("replay StreamConversation: %v", err)
	}
	replayed, err := replay.Recv()
	if err != nil {
		t.Fatalf("replay recv: %v", err)
	}
	if replayed.Seq != 1 || replayed.Type != "reasoning_delta" || replayed.ItemId != "reason_1" {
		t.Fatalf("replayed delta = %#v, want persisted reasoning_delta with item id", replayed)
	}
}

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
