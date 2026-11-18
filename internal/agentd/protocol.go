package agentd

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

const (
	SendBuffer                   = 128
	HeartbeatInterval            = 15 * time.Second
	InboundStaleTimeout          = 50 * time.Second
	InboundStaleCheckEvery       = 10 * time.Second
	MetaFilename                 = ".agent-meta.json"
	RecentCheckRunLimit          = 256
	MaxPendingEvents             = 10000
	TurnCompleteType             = "turn_complete"
	TranscriptSeedLimit          = 24000
	DefaultDetachAfter           = 2 * time.Minute
	DetachCheckEvery             = 5 * time.Second
	StandbyPollDefault           = 30 * time.Second
	StandbyPollMin               = 5 * time.Second
	StandbyPollMax               = 5 * time.Minute
	StandbyCallTimeout           = 20 * time.Second
	ChildKeyTTLSeconds     int64 = 12 * 3600
	ChildKeyRenewWindow          = 10 * time.Minute
)

var ErrAgentDetached = errors.New("agent daemon detached")

type SendQueue struct {
	Ch      chan *corev1.DaemonMessage
	Done    chan struct{}
	mu      sync.Mutex
	sendErr error
}

func NewSendQueue(ctx context.Context, stream corev1.AgentService_ConnectClient, buffer int) *SendQueue {
	q := &SendQueue{
		Ch:   make(chan *corev1.DaemonMessage, buffer),
		Done: make(chan struct{}),
	}
	go func() {
		defer close(q.Done)
		for {
			select {
			case <-ctx.Done():
				_ = stream.CloseSend()
				q.setErr(ctx.Err())
				return
			case msg := <-q.Ch:
				if msg == nil {
					continue
				}
				if err := stream.Send(msg); err != nil {
					q.setErr(err)
					return
				}
			}
		}
	}()
	return q
}

func (q *SendQueue) Send(ctx context.Context, msg *corev1.DaemonMessage) error {
	select {
	case <-q.Done:
		if err := q.Err(); err != nil {
			return err
		}
		return io.ErrClosedPipe
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-q.Done:
		if err := q.Err(); err != nil {
			return err
		}
		return io.ErrClosedPipe
	case q.Ch <- msg:
		return nil
	}
}

func (q *SendQueue) Wait() error {
	<-q.Done
	return q.Err()
}

func (q *SendQueue) setErr(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sendErr = err
}

func (q *SendQueue) Err() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.sendErr
}

type InboundState struct {
	mu   sync.Mutex
	last time.Time
}

func NewInboundState(now time.Time) *InboundState {
	return &InboundState{last: now}
}

func (s *InboundState) Mark(now time.Time) {
	s.mu.Lock()
	s.last = now
	s.mu.Unlock()
}

func (s *InboundState) LastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func InboundStale(last time.Time, now time.Time, timeout time.Duration) bool {
	if last.IsZero() || timeout <= 0 || now.Before(last) {
		return false
	}
	return now.Sub(last) > timeout
}

func RegisterDaemonMessage(name, runtimeName, version, instanceID string, concurrency int, allowHostExec bool) *corev1.DaemonMessage {
	name = ResolvedAgentName(name)
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	return &corev1.DaemonMessage{
		Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
			Name:              name,
			Runtime:           runtimeName,
			Version:           version,
			ContainerRuntimes: DetectContainerRuntimes(),
			AllowHostExec:     allowHostExec,
			InstanceId:        instanceID,
			CheckConcurrency:  int32(concurrency),
		}},
	}
}

func HeartbeatDaemonMessage() *corev1.DaemonMessage {
	return &corev1.DaemonMessage{
		Payload: &corev1.DaemonMessage_Heartbeat{Heartbeat: &corev1.Heartbeat{}},
	}
}

func DetachDaemonMessage() *corev1.DaemonMessage {
	return &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Detach{Detach: &corev1.Detach{}}}
}

func CheckSlotsFromEnv() chan struct{} {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("GITSLICE_CHECK_CONCURRENCY")))
	if err != nil || n <= 0 {
		return nil
	}
	return make(chan struct{}, n)
}

func NewInstanceID() string {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return fmt.Sprintf("p%d", os.Getpid())
	}
	return hex.EncodeToString(b[:])
}

func ConversationPaths(root, conversationID string) (string, string, error) {
	if strings.TrimSpace(conversationID) == "" {
		return "", "", NewError("invalid_conversation_id", "agent conversation id is empty", "Try creating a new conversation.")
	}
	if filepath.IsAbs(conversationID) || conversationID == "." || conversationID == ".." || strings.ContainsAny(conversationID, `/\`) {
		return "", "", NewError("invalid_conversation_id", "invalid agent conversation id: "+conversationID, "Try creating a new conversation.")
	}
	subdir := filepath.ToSlash(filepath.Join("conversations", conversationID))
	return subdir, filepath.Join(root, "conversations", conversationID), nil
}

func StandbyRetryWait(failures int) time.Duration {
	return min(StandbyPollDefault<<min(max(failures-1, 0), 8), StandbyPollMax)
}

func StandbyPollOverride() (time.Duration, bool) {
	value := strings.TrimSpace(os.Getenv("GITSLICE_AGENT_POLL_INTERVAL"))
	if value == "" {
		return 0, false
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 10*time.Millisecond {
		return 0, false
	}
	return parsed, true
}

func IsStandbyUnsupported(err error) bool {
	switch grpcstatus.Code(err) {
	case codes.Unimplemented, codes.FailedPrecondition:
		return true
	}
	return false
}

func RenderTranscript(events []*corev1.ConversationEvent) string {
	lines := make([]string, 0, len(events))
	for _, ev := range events {
		text := strings.TrimSpace(ev.GetText())
		if text == "" {
			continue
		}
		var label string
		switch ev.GetType() {
		case "message":
			switch ev.GetRole() {
			case "user":
				label = "User"
			case "agent":
				label = "Assistant"
			default:
				label = "System"
			}
		case "tool_call":
			label = "Tool call"
		default:
			continue
		}
		lines = append(lines, fmt.Sprintf("[%s] %s", label, text))
	}
	if len(lines) == 0 {
		return ""
	}
	body := strings.Join(lines, "\n\n")
	if len(body) > TranscriptSeedLimit {
		body = strings.ToValidUTF8(body[len(body)-TranscriptSeedLimit:], "")
	}
	return "You are resuming an existing conversation. The transcript so far is " +
		"below; use it as context and do not repeat work already completed.\n\n" +
		"--- transcript start ---\n" + body + "\n--- transcript end ---"
}
