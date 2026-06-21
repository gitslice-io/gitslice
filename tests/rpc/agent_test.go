package rpc_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
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
