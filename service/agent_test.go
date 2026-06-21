package service

import (
	"context"
	"io"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/metadata"
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
