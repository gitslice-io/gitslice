package rpc_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
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

// TestGetConversationEventsLimitTailsAndPagesHistory covers the tail-loading
// contract the web transcript relies on: a positive limit returns the newest N
// events in the window (ascending by seq), and lowering before_seq pages the
// preceding N — so the client can open on the last messages and walk older ones
// in as the user scrolls up.
func TestGetConversationEventsLimitTailsAndPagesHistory(t *testing.T) {
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
		Name:    "tail-daemon",
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
		Title:    "tail paging",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	const total = 5
	for i := 1; i <= total; i++ {
		if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Event{Event: &corev1.AgentEvent{
			ConversationId: conv.Id,
			Role:           "agent",
			Type:           "message",
			Text:           fmt.Sprintf("msg-%d", i),
			ItemId:         fmt.Sprintf("item_%d", i),
		}}}); err != nil {
			t.Fatalf("send event %d: %v", i, err)
		}
	}

	// The daemon->server stream persists asynchronously, so poll until all events
	// land before asserting on the tail window.
	deadline := time.Now().Add(5 * time.Second)
	for {
		all, err := agent.GetConversationEvents(ctx, &corev1.GetConversationEventsRequest{ConversationId: conv.Id})
		if err != nil {
			t.Fatalf("GetConversationEvents (all): %v", err)
		}
		if len(all.Events) == total {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("persisted %d events, want %d", len(all.Events), total)
		}
		time.Sleep(25 * time.Millisecond)
	}

	// A positive limit returns the newest two events, still ascending by seq.
	tail, err := agent.GetConversationEvents(ctx, &corev1.GetConversationEventsRequest{ConversationId: conv.Id, Limit: 2})
	if err != nil {
		t.Fatalf("GetConversationEvents (tail): %v", err)
	}
	if got := eventTexts(tail.Events); !reflect.DeepEqual(got, []string{"msg-4", "msg-5"}) {
		t.Fatalf("tail texts = %v, want [msg-4 msg-5]", got)
	}
	if tail.Events[0].Seq >= tail.Events[1].Seq {
		t.Fatalf("tail not ascending by seq: %d, %d", tail.Events[0].Seq, tail.Events[1].Seq)
	}

	// Paging older: fetch the two events preceding the tail window by lowering
	// before_seq below the oldest event we already hold.
	older, err := agent.GetConversationEvents(ctx, &corev1.GetConversationEventsRequest{
		ConversationId: conv.Id,
		BeforeSeq:      tail.Events[0].Seq - 1,
		Limit:          2,
	})
	if err != nil {
		t.Fatalf("GetConversationEvents (older): %v", err)
	}
	if got := eventTexts(older.Events); !reflect.DeepEqual(got, []string{"msg-2", "msg-3"}) {
		t.Fatalf("older page texts = %v, want [msg-2 msg-3]", got)
	}
}

func eventTexts(events []*corev1.ConversationEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Text)
	}
	return out
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

// TestAgentUserMessageRedeliveredAfterReconnect reproduces a prod incident:
// a daemon disconnects while a user message is queued, and the message should
// be redelivered on reconnect via redelivery-on-ConversationStarted.
func TestAgentUserMessageRedeliveredAfterReconnect(t *testing.T) {
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
		Name:    "redelivery-daemon",
		Runtime: "test",
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

	// Daemon should appear online.
	daemons, err := agent.ListDaemons(ctx, &corev1.ListDaemonsRequest{})
	if err != nil {
		t.Fatalf("ListDaemons: %v", err)
	}
	if len(daemons.Daemons) != 1 || daemons.Daemons[0].Id != daemonID || daemons.Daemons[0].Status != "online" {
		t.Fatalf("ListDaemons = %#v, want one online daemon %s", daemons.Daemons, daemonID)
	}

	// Web side creates a conversation for the daemon.
	conv, err := agent.CreateConversation(ctx, &corev1.CreateConversationRequest{
		DaemonId: daemonID,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "backend"},
		Title:    "redelivery test",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if conv.Id == "" || conv.DaemonId != daemonID {
		t.Fatalf("unexpected conversation %#v", conv)
	}

	// Drain StartConversation and reply ConversationStarted.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv StartConversation: %v", err)
		}
		if msg.GetStart() != nil && msg.GetStart().ConversationId == conv.Id {
			break
		}
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
		ConversationId: conv.Id,
	}}}); err != nil {
		t.Fatalf("send ConversationStarted: %v", err)
	}

	// Drain any follow-up messages (redelivery check, ReconcileWorkspaces, Pings)
	// for a bounded window. Recv blocks until the next server message (pings are
	// 20s apart), so pump it on a goroutine and bound the window with a timer;
	// the stream is never read directly again after this (the daemon context is
	// cancelled below), so the pump cannot race another Recv.
	drainMsgs := make(chan *corev1.ServerMessage, 8)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				close(drainMsgs)
				return
			}
			drainMsgs <- msg
		}
	}()
	drainWindow := time.After(2 * time.Second)
	for {
		select {
		case <-drainWindow:
			goto drained
		case msg, ok := <-drainMsgs:
			if !ok {
				t.Fatalf("drain recv: stream ended unexpectedly")
			}
			// Skip Pings, ReconcileWorkspaces, EventAck.
			if msg.GetPing() != nil || msg.GetReconcile() != nil || msg.GetAck() != nil {
				continue
			}
			// Any StartConversation is just the redelivery check (no messages).
			if msg.GetStart() != nil {
				continue
			}
			t.Fatalf("unexpected drain message after Started: %#v", msg)
		}
	}
drained:

	// Daemon disconnects: cancel context and wait until server notices.
	cancelDaemon()
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		convCheck, err := agent.GetConversation(ctx, &corev1.GetConversationRequest{ConversationId: conv.Id})
		if err != nil {
			t.Fatalf("GetConversation during poll: %v", err)
		}
		if !convCheck.DaemonOnline {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Verify daemon is offline.
	convCheck, err := agent.GetConversation(ctx, &corev1.GetConversationRequest{ConversationId: conv.Id})
	if err != nil {
		t.Fatalf("GetConversation after disconnect: %v", err)
	}
	if convCheck.DaemonOnline {
		t.Fatalf("daemon still online after cancel, want offline")
	}

	// Send agent message while daemon is offline.
	resp, err := agent.SendAgentMessage(ctx, &corev1.SendAgentMessageRequest{
		ConversationId: conv.Id,
		Text:           "lost while offline",
	})
	if err != nil {
		t.Fatalf("SendAgentMessage while offline: %v", err)
	}
	userSeq := resp.Event.Seq
	if userSeq == 0 {
		t.Fatalf("expected user seq, got %#v", resp.Event)
	}

	// Daemon reconnects with fresh Connect stream + register (same name → same daemon id).
	daemonCtx2, cancelDaemon2 := context.WithCancel(ctx)
	defer cancelDaemon2()
	stream2, err := agent.Connect(daemonCtx2)
	if err != nil {
		t.Fatalf("reconnect Connect: %v", err)
	}
	if err := stream2.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name:    "redelivery-daemon",
		Runtime: "test",
	}}}); err != nil {
		t.Fatalf("reconnect send register: %v", err)
	}
	reg2, err := stream2.Recv()
	if err != nil {
		t.Fatalf("reconnect recv registered: %v", err)
	}
	daemonID2 := reg2.GetRegistered().GetDaemonId()
	if daemonID2 != daemonID {
		t.Fatalf("reconnect daemon id changed: %s -> %s", daemonID, daemonID2)
	}

	// Server replays StartConversation; daemon replies ConversationStarted.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := stream2.Recv()
		if err != nil {
			t.Fatalf("reconnect recv StartConversation: %v", err)
		}
		if msg.GetStart() != nil && msg.GetStart().ConversationId == conv.Id {
			break
		}
	}
	if err := stream2.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
		ConversationId: conv.Id,
	}}}); err != nil {
		t.Fatalf("reconnect send ConversationStarted: %v", err)
	}

	// Assert daemon receives DeliverUserMessage with the lost message.
	deadline = time.Now().Add(10 * time.Second)
	var gotDeliver *corev1.DeliverUserMessage
	for time.Now().Before(deadline) {
		msg, err := stream2.Recv()
		if err != nil {
			t.Fatalf("recv DeliverUserMessage: %v", err)
		}
		um := msg.GetUserMessage()
		if um == nil {
			// Skip Pings, ReconcileWorkspaces, EventAck.
			if msg.GetPing() != nil || msg.GetReconcile() != nil || msg.GetAck() != nil {
				continue
			}
			t.Fatalf("unexpected message while waiting for DeliverUserMessage: %#v", msg)
		}
		if um.ConversationId == conv.Id && um.Text == "lost while offline" {
			gotDeliver = um
			break
		}
	}
	if gotDeliver == nil {
		t.Fatalf("did not receive DeliverUserMessage with seq %d", userSeq)
	}
	if gotDeliver.Seq != userSeq {
		t.Fatalf("DeliverUserMessage seq = %d, want %d", gotDeliver.Seq, userSeq)
	}

	// Reply ConversationStarted AGAIN (duplicate). Server redelivery is stateless:
	// it will resend the same user message. Daemon-side seq dedup makes it safe.
	if err := stream2.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
		ConversationId: conv.Id,
	}}}); err != nil {
		t.Fatalf("send duplicate ConversationStarted: %v", err)
	}

	// Assert second redelivery arrives (server redelivery is at-least-once).
	deadline = time.Now().Add(5 * time.Second)
	var gotSecondDeliver *corev1.DeliverUserMessage
	for time.Now().Before(deadline) {
		msg, err := stream2.Recv()
		if err != nil {
			t.Fatalf("recv second DeliverUserMessage: %v", err)
		}
		um := msg.GetUserMessage()
		if um == nil {
			// Skip Pings, ReconcileWorkspaces, EventAck.
			if msg.GetPing() != nil || msg.GetReconcile() != nil || msg.GetAck() != nil {
				continue
			}
			t.Fatalf("unexpected message while waiting for second DeliverUserMessage: %#v", msg)
		}
		if um.ConversationId == conv.Id && um.Text == "lost while offline" && um.Seq == userSeq {
			gotSecondDeliver = um
			break
		}
	}
	if gotSecondDeliver == nil {
		t.Fatalf("did not receive second DeliverUserMessage (at-least-once redelivery)")
	}

	// Daemon sends an AgentEvent answering the message.
	if err := stream2.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Event{Event: &corev1.AgentEvent{
		ConversationId: conv.Id,
		Role:           "agent",
		Type:           "message",
		Text:           "got it",
		ClientSeq:      1,
	}}}); err != nil {
		t.Fatalf("send AgentEvent: %v", err)
	}

	// Reply ConversationStarted a third time and assert NO DeliverUserMessage arrives
	// (user message is now answered, UnansweredUserEvents is empty).
	if err := stream2.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
		ConversationId: conv.Id,
	}}}); err != nil {
		t.Fatalf("send third ConversationStarted: %v", err)
	}

	// Verify no DeliverUserMessage arrives within a bounded window. Same pump
	// pattern as the drain above: stream2 is not read again after this, so the
	// reader goroutine cannot race another Recv.
	finalMsgs := make(chan *corev1.ServerMessage, 8)
	go func() {
		for {
			msg, err := stream2.Recv()
			if err != nil {
				close(finalMsgs)
				return
			}
			finalMsgs <- msg
		}
	}()
	noRedeliveryWindow := time.After(500 * time.Millisecond)
	for {
		select {
		case <-noRedeliveryWindow:
			return
		case msg, ok := <-finalMsgs:
			if !ok {
				t.Fatalf("recv while checking no redelivery: stream ended unexpectedly")
			}
			um := msg.GetUserMessage()
			if um != nil && um.ConversationId == conv.Id && um.Seq == userSeq {
				t.Fatalf("unexpected third redelivery for seq %d after answer", userSeq)
			}
			// Skip Pings, ReconcileWorkspaces, EventAck.
			if msg.GetPing() != nil || msg.GetReconcile() != nil || msg.GetAck() != nil {
				continue
			}
			// StartConversation may arrive again, ignore it.
			if msg.GetStart() != nil {
				continue
			}
			t.Fatalf("unexpected message while checking no redelivery: %#v", msg)
		}
	}
}
