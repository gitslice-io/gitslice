package service

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestAgentServicePersistsRuntimeDeltas(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	stream := newFakeAgentConnectStream(ctx)

	errCh := make(chan error, 1)
	go func() {
		errCh <- handlers.Agent.Connect(stream)
	}()

	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name:    "delta-daemon",
		Runtime: "test",
	}}}

	registered := (<-stream.sent).GetRegistered()
	if registered == nil || registered.DaemonId == "" {
		t.Fatalf("registered message = %#v, want daemon id", registered)
	}

	conv, err := handlers.Agent.CreateConversation(ctx, &corev1.CreateConversationRequest{
		DaemonId: registered.DaemonId,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "home"},
		Title:    "delta persistence",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Event{Event: &corev1.AgentEvent{
		ConversationId: conv.Id,
		Role:           "agent",
		Type:           "reasoning_delta",
		Text:           "checking",
		ItemId:         "reason_1",
		Ephemeral:      true,
	}}}
	close(stream.recv)

	if err := <-errCh; err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	events, err := handlers.Agent.GetConversationEvents(ctx, &corev1.GetConversationEventsRequest{ConversationId: conv.Id})
	if err != nil {
		t.Fatalf("GetConversationEvents: %v", err)
	}
	if len(events.Events) != 1 {
		t.Fatalf("events = %d, want 1 (%#v)", len(events.Events), events.Events)
	}
	ev := events.Events[0]
	if ev.Seq != 1 || ev.Type != "reasoning_delta" || ev.Text != "checking" || ev.ItemId != "reason_1" {
		t.Fatalf("persisted delta = %#v, want seq 1 reasoning_delta with item id", ev)
	}
}

func TestAgentServiceDedupsAndAcksClientSeq(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	stream := newFakeAgentConnectStream(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- handlers.Agent.Connect(stream) }()

	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name: "ack-daemon", Runtime: "test",
	}}}
	registered := (<-stream.sent).GetRegistered()
	if registered == nil || registered.DaemonId == "" {
		t.Fatalf("registered message = %#v, want daemon id", registered)
	}

	conv, err := handlers.Agent.CreateConversation(ctx, &corev1.CreateConversationRequest{
		DaemonId: registered.DaemonId,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "home"},
		Title:    "ack",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	event := &corev1.AgentEvent{
		ConversationId: conv.Id,
		Role:           "agent",
		Type:           "message",
		Text:           "hello",
		ItemId:         "m1",
		ClientSeq:      7,
		Final:          true,
	}
	// Send the same sequenced event twice; the resend must be deduped but still acked.
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Event{Event: event}}
	if got := waitForAck(t, stream); got != 7 {
		t.Fatalf("first ack acked_client_seq = %d, want 7", got)
	}
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Event{Event: event}}
	if got := waitForAck(t, stream); got != 7 {
		t.Fatalf("second ack acked_client_seq = %d, want 7", got)
	}
	close(stream.recv)
	if err := <-errCh; err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	events, err := handlers.Agent.GetConversationEvents(ctx, &corev1.GetConversationEventsRequest{ConversationId: conv.Id})
	if err != nil {
		t.Fatalf("GetConversationEvents: %v", err)
	}
	if len(events.Events) != 1 {
		t.Fatalf("events = %d, want 1 (resend deduped) (%#v)", len(events.Events), events.Events)
	}
	if ev := events.Events[0]; ev.Seq != 1 || ev.ClientSeq != 7 || ev.Text != "hello" {
		t.Fatalf("persisted event = %#v, want seq 1 client_seq 7 'hello'", ev)
	}
}

func TestAgentServiceRedeliversOfflineUserMessageAndSequencesLiveSend(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	daemon, err := mem.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: "user_alice",
		Account:   "acme",
		Name:      "redelivery-daemon",
		Runtime:   "test",
	})
	if err != nil {
		t.Fatalf("RegisterDaemon: %v", err)
	}
	conv, err := handlers.Agent.CreateConversation(ctx, &corev1.CreateConversationRequest{
		DaemonId: daemon.Id,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "home"},
		Title:    "redelivery",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	offline, err := handlers.Agent.SendAgentMessage(ctx, &corev1.SendAgentMessageRequest{
		ConversationId: conv.Id,
		Text:           "sent while offline",
	})
	if err != nil {
		t.Fatalf("SendAgentMessage offline: %v", err)
	}
	if offline.Event.GetSeq() != 1 {
		t.Fatalf("offline event seq = %d, want 1", offline.Event.GetSeq())
	}

	stream := newFakeAgentConnectStream(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- handlers.Agent.Connect(stream) }()
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name: "redelivery-daemon", Runtime: "test",
	}}}
	registered := (<-stream.sent).GetRegistered()
	if registered.GetDaemonId() != daemon.Id {
		t.Fatalf("registered daemon id = %q, want %q", registered.GetDaemonId(), daemon.Id)
	}
	start := waitForStart(t, stream)
	if start.GetConversationId() != conv.Id {
		t.Fatalf("replayed conversation id = %q, want %q", start.GetConversationId(), conv.Id)
	}

	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
		ConversationId: conv.Id,
	}}}
	redelivered := waitForUserMessage(t, stream)
	if redelivered.GetConversationId() != conv.Id || redelivered.GetText() != "sent while offline" || redelivered.GetSeq() != offline.Event.GetSeq() {
		t.Fatalf("redelivered user message = %#v, want conversation %s text %q seq %d", redelivered, conv.Id, "sent while offline", offline.Event.GetSeq())
	}

	live, err := handlers.Agent.SendAgentMessage(ctx, &corev1.SendAgentMessageRequest{
		ConversationId: conv.Id,
		Text:           "sent while connected",
	})
	if err != nil {
		t.Fatalf("SendAgentMessage connected: %v", err)
	}
	delivered := waitForUserMessage(t, stream)
	if delivered.GetText() != "sent while connected" || delivered.GetSeq() != live.Event.GetSeq() || delivered.GetSeq() != 2 {
		t.Fatalf("live user message = %#v, want text %q seq 2", delivered, "sent while connected")
	}

	close(stream.recv)
	if err := <-errCh; err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
}

func TestAgentServiceConversationStartedDoesNotRedeliverForDifferentDaemon(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	owner, err := mem.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: "user_alice", Account: "acme", Name: "owner-daemon", Runtime: "test",
	})
	if err != nil {
		t.Fatalf("RegisterDaemon owner: %v", err)
	}
	other, err := mem.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: "user_alice", Account: "acme", Name: "other-daemon", Runtime: "test",
	})
	if err != nil {
		t.Fatalf("RegisterDaemon other: %v", err)
	}
	conv, err := handlers.Agent.CreateConversation(ctx, &corev1.CreateConversationRequest{
		DaemonId: owner.Id,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "home"},
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := handlers.Agent.SendAgentMessage(ctx, &corev1.SendAgentMessageRequest{
		ConversationId: conv.Id,
		Text:           "owner only",
	}); err != nil {
		t.Fatalf("SendAgentMessage: %v", err)
	}

	stream := newFakeAgentConnectStream(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- handlers.Agent.Connect(stream) }()
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name: "other-daemon", Runtime: "test",
	}}}
	registered := (<-stream.sent).GetRegistered()
	if registered.GetDaemonId() != other.Id {
		t.Fatalf("registered daemon id = %q, want %q", registered.GetDaemonId(), other.Id)
	}
	if err := mem.Agents.SetDaemonStatus(ctx, other.Id, "offline"); err != nil {
		t.Fatalf("SetDaemonStatus offline: %v", err)
	}
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
		ConversationId: conv.Id,
	}}}
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Heartbeat{Heartbeat: &corev1.Heartbeat{}}}
	waitForDaemonStatus(t, mem, other.Id, "online")
	assertNoUserMessage(t, stream)

	close(stream.recv)
	if err := <-errCh; err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
}

func TestAgentServiceCloseConversationMarksInactiveAndTearsDown(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	stream := newFakeAgentConnectStream(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- handlers.Agent.Connect(stream) }()

	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name: "close-daemon", Runtime: "test",
	}}}
	registered := (<-stream.sent).GetRegistered()
	if registered == nil || registered.DaemonId == "" {
		t.Fatalf("registered message = %#v, want daemon id", registered)
	}

	conv, err := handlers.Agent.CreateConversation(ctx, &corev1.CreateConversationRequest{
		DaemonId: registered.DaemonId,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "home"},
		Title:    "close me",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	closed, err := handlers.Agent.CloseConversation(ctx, &corev1.CloseConversationRequest{ConversationId: conv.Id})
	if err != nil {
		t.Fatalf("CloseConversation: %v", err)
	}
	if closed.Status != "inactive" {
		t.Fatalf("closed.Status = %q, want inactive", closed.Status)
	}

	closeMsg := waitForClose(t, stream)
	if closeMsg.GetConversationId() != conv.Id || !closeMsg.GetDeleteWorkspace() {
		t.Fatalf("close message = %#v, want conversation %s delete_workspace true", closeMsg, conv.Id)
	}

	close(stream.recv)
	if err := <-errCh; err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	got, err := handlers.Agent.GetConversation(ctx, &corev1.GetConversationRequest{ConversationId: conv.Id})
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.Status != "inactive" {
		t.Fatalf("persisted status = %q, want inactive", got.Status)
	}
}

// waitForClose reads server->daemon messages until it sees a CloseWorkspace,
// ignoring other traffic (StartConversation from CreateConversation, etc.).
func waitForClose(t *testing.T, stream *fakeAgentConnectStream) *corev1.CloseWorkspace {
	t.Helper()
	for {
		select {
		case msg := <-stream.sent:
			if c := msg.GetClose(); c != nil {
				return c
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for CloseWorkspace")
		}
	}
}

// waitForAck reads server->daemon messages until it sees an EventAck, ignoring
// other traffic (StartConversation from CreateConversation, etc.).
func waitForAck(t *testing.T, stream *fakeAgentConnectStream) int64 {
	t.Helper()
	for {
		select {
		case msg := <-stream.sent:
			if ack := msg.GetAck(); ack != nil {
				return ack.GetAckedClientSeq()
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for EventAck")
		}
	}
}

func waitForStart(t *testing.T, stream *fakeAgentConnectStream) *corev1.StartConversation {
	t.Helper()
	for {
		select {
		case msg := <-stream.sent:
			if start := msg.GetStart(); start != nil {
				return start
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for StartConversation")
		}
	}
}

func waitForUserMessage(t *testing.T, stream *fakeAgentConnectStream) *corev1.DeliverUserMessage {
	t.Helper()
	for {
		select {
		case msg := <-stream.sent:
			if userMessage := msg.GetUserMessage(); userMessage != nil {
				return userMessage
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for DeliverUserMessage")
		}
	}
}

func waitForDaemonStatus(t *testing.T, mem *memory.Stores, daemonID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		daemon, err := mem.Agents.GetDaemon(context.Background(), daemonID)
		if err == nil && daemon.GetStatus() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for daemon %s status %q", daemonID, want)
}

func assertNoUserMessage(t *testing.T, stream *fakeAgentConnectStream) {
	t.Helper()
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case msg := <-stream.sent:
			if userMessage := msg.GetUserMessage(); userMessage != nil {
				t.Fatalf("unexpected DeliverUserMessage: %#v", userMessage)
			}
		case <-timer.C:
			return
		}
	}
}

type fakeAgentConnectStream struct {
	ctx  context.Context
	recv chan *corev1.DaemonMessage
	sent chan *corev1.ServerMessage
}

func newFakeAgentConnectStream(ctx context.Context) *fakeAgentConnectStream {
	return &fakeAgentConnectStream{
		ctx:  ctx,
		recv: make(chan *corev1.DaemonMessage, 8),
		sent: make(chan *corev1.ServerMessage, 8),
	}
}

func (s *fakeAgentConnectStream) Send(message *corev1.ServerMessage) error {
	s.sent <- message
	return nil
}

func (s *fakeAgentConnectStream) Recv() (*corev1.DaemonMessage, error) {
	message, ok := <-s.recv
	if !ok {
		return nil, io.EOF
	}
	return message, nil
}

func (s *fakeAgentConnectStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeAgentConnectStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeAgentConnectStream) SetTrailer(metadata.MD)       {}
func (s *fakeAgentConnectStream) Context() context.Context     { return s.ctx }
func (s *fakeAgentConnectStream) SendMsg(any) error            { return nil }
func (s *fakeAgentConnectStream) RecvMsg(any) error            { return nil }

// TestSetSliceCIDaemonNeedsADaemonTheCallerMayManage: a CI daemon runs a
// slice's checks and receives its secrets, so an owner of a slice cannot name
// a daemon that belongs to someone else.
func TestSetSliceCIDaemonNeedsADaemonTheCallerMayManage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := authctx.WithSubjectID(context.Background(), "user_alice")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	slice := mem.PutSlice(sliceRef, []string{"/acme/payment"}, "private")

	mine, err := mem.Agents.RegisterDaemon(alice, storage.AgentDaemonInput{SubjectID: "user_alice", Account: "acme", Name: "alice-ci", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	// Someone else's daemon, registered under an account alice has no part in.
	bob := authctx.WithSubjectID(context.Background(), "user_bob")
	theirs, err := mem.Agents.RegisterDaemon(bob, storage.AgentDaemonInput{SubjectID: "user_bob", Account: "bob", Name: "bob-ci", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	set := func(daemonID string) error {
		_, err := handlers.Slice.SetSliceCIDaemon(alice, &corev1.SetSliceCIDaemonRequest{Slice: sliceRef, DaemonId: daemonID})
		return err
	}
	current := func() string {
		t.Helper()
		got, err := mem.Slices.Get(alice, slice.Id)
		if err != nil {
			t.Fatal(err)
		}
		return got.CiDaemonId
	}

	// Another person's daemon looks exactly like one that does not exist.
	for _, id := range []string{theirs.Id, "agent_does_not_exist"} {
		err := set(id)
		if status.Code(err) != codes.NotFound || !strings.Contains(err.Error(), "daemon not found") {
			t.Fatalf("naming %s: err = %v, want NotFound daemon not found", id, err)
		}
	}
	if got := current(); got != "" {
		t.Fatalf("a refused request left CI daemon %q", got)
	}

	// Her own is fine, and clearing always is.
	if err := set(mine.Id); err != nil || current() != mine.Id {
		t.Fatalf("naming her own daemon: err = %v, current = %q", err, current())
	}
	if err := set(""); err != nil || current() != "" {
		t.Fatalf("clearing: err = %v, current = %q", err, current())
	}

	// A slice that already names a daemon keeps it when its owner saves the
	// same value, even if they could not name it now.
	if _, err := mem.Slices.SetCIDaemon(alice, slice.Id, theirs.Id); err != nil {
		t.Fatal(err)
	}
	if err := set(theirs.Id); err != nil || current() != theirs.Id {
		t.Fatalf("saving the unchanged daemon: err = %v, current = %q", err, current())
	}
	// But she cannot move it to another one she may not use, and can move it to hers.
	if err := set("agent_does_not_exist"); status.Code(err) != codes.NotFound {
		t.Fatalf("moving to a daemon that is not hers: err = %v", err)
	}
	if err := set(mine.Id); err != nil || current() != mine.Id {
		t.Fatalf("moving to her own daemon: err = %v, current = %q", err, current())
	}
}

// TestExposingADaemonToASliceLetsItsOwnersChooseIt: the owner of a daemon
// decides which slices it is exposed to, and a slice's owner may then choose it
// as the slice's CI daemon, until the owner withdraws it.
func TestExposingADaemonToASliceLetsItsOwnersChooseIt(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := authctx.WithSubjectID(context.Background(), "user_alice")
	bob := authctx.WithSubjectID(context.Background(), "user_bob")
	mem.AddAccount("user_bob", "bob")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	slice := mem.PutSlice(sliceRef, []string{"/acme/payment"}, "public")
	theirs, err := mem.Agents.RegisterDaemon(bob, storage.AgentDaemonInput{SubjectID: "user_bob", Account: "bob", Name: "bob-ci", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	set := func(id string) error {
		_, err := handlers.Slice.SetSliceCIDaemon(alice, &corev1.SetSliceCIDaemonRequest{Slice: sliceRef, DaemonId: id})
		return err
	}
	listFor := func(ctx context.Context) []*corev1.AgentDaemon {
		t.Helper()
		res, err := handlers.Agent.ListDaemons(ctx, &corev1.ListDaemonsRequest{ForSlice: sliceRef})
		if err != nil {
			t.Fatal(err)
		}
		return res.Daemons
	}
	has := func(daemons []*corev1.AgentDaemon, id string) *corev1.AgentDaemon {
		for _, d := range daemons {
			if d.Id == id {
				return d
			}
		}
		return nil
	}

	// Not exposed: alice cannot see or choose it.
	if has(listFor(alice), theirs.Id) != nil {
		t.Fatal("alice sees a daemon nobody exposed to her slice")
	}
	if err := set(theirs.Id); status.Code(err) != codes.NotFound {
		t.Fatalf("choosing an unexposed daemon: %v; want NotFound", err)
	}
	// Alice cannot expose it, and a stranger cannot either.
	if _, err := handlers.Agent.ExposeDaemon(alice, &corev1.ExposeDaemonRequest{DaemonId: theirs.Id, Slice: sliceRef}); status.Code(err) != codes.NotFound {
		t.Fatalf("alice exposing bob's daemon: %v; want NotFound", err)
	}

	// Bob exposes it to alice's slice.
	exposed, err := handlers.Agent.ExposeDaemon(bob, &corev1.ExposeDaemonRequest{DaemonId: theirs.Id, Slice: sliceRef})
	if err != nil || !exposed.Manageable || len(exposed.ExposedTo) != 1 || exposed.ExposedTo[0].Slice != "payment" {
		t.Fatalf("ExposeDaemon = %+v, %v", exposed, err)
	}
	if _, err := handlers.Agent.ExposeDaemon(bob, &corev1.ExposeDaemonRequest{DaemonId: theirs.Id, Slice: sliceRef}); err != nil {
		t.Fatalf("exposing twice: %v", err)
	}
	// Now alice sees it for that slice, as one she cannot manage, and chooses it.
	seen := has(listFor(alice), theirs.Id)
	if seen == nil || seen.Manageable || seen.Mine || len(seen.ExposedTo) != 0 {
		t.Fatalf("alice's view of the exposed daemon = %+v", seen)
	}
	if res, _ := handlers.Agent.ListDaemons(alice, &corev1.ListDaemonsRequest{}); has(res.Daemons, theirs.Id) != nil {
		t.Fatal("an exposed daemon shows in the plain list of someone who does not manage it")
	}
	if err := set(theirs.Id); err != nil {
		t.Fatalf("choosing an exposed daemon: %v", err)
	}
	if got := handlers.Agent.dispatcher.ciDaemonRefusal(alice, slice.Id, theirs.Id); got != "" {
		t.Fatalf("an exposed daemon is refused: %s", got)
	}

	// Bob withdraws it: the slice keeps naming it, but its checks are refused.
	if _, err := handlers.Agent.UnexposeDaemon(bob, &corev1.UnexposeDaemonRequest{DaemonId: theirs.Id, Slice: sliceRef}); err != nil {
		t.Fatal(err)
	}
	if got := handlers.Agent.dispatcher.ciDaemonRefusal(alice, slice.Id, theirs.Id); got == "" {
		t.Fatal("a withdrawn daemon still runs the slice's checks")
	}
	if has(listFor(alice), theirs.Id) != nil {
		t.Fatal("alice still sees a withdrawn daemon")
	}

	// Choosing a daemon of her own exposes it too, so its checks run.
	mine, err := mem.Agents.RegisterDaemon(alice, storage.AgentDaemonInput{SubjectID: "user_alice", Account: "acme", Name: "alice-ci", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if err := set(mine.Id); err != nil {
		t.Fatal(err)
	}
	if got := handlers.Agent.dispatcher.ciDaemonRefusal(alice, slice.Id, mine.Id); got != "" {
		t.Fatalf("her own chosen daemon is refused: %s", got)
	}
	// A private slice cannot be named by someone who cannot read it.
	private := &corev1.SliceRef{Account: "acme", Slice: "secret"}
	mem.PutSlice(private, []string{"/acme/secret"}, "private")
	if _, err := handlers.Agent.ExposeDaemon(bob, &corev1.ExposeDaemonRequest{DaemonId: theirs.Id, Slice: private}); err == nil {
		t.Fatal("bob exposed a daemon to a private slice he cannot read")
	}
}
