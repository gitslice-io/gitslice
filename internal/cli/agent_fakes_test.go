package cli

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type fakeAgentAuthClient struct {
	mu          sync.Mutex
	createReqs  []*corev1.CreateApiKeyRequest
	revokeReqs  []*corev1.RevokeApiKeyRequest
	createErr   error
	revokeErr   error
	expiresAtFn func(ttlSeconds int64) string
	seq         int
}

func (f *fakeAgentAuthClient) CreateApiKey(_ context.Context, in *corev1.CreateApiKeyRequest, _ ...grpc.CallOption) (*corev1.CreateApiKeyResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.seq++
	f.createReqs = append(f.createReqs, in)
	id := fmt.Sprintf("key_fake_%d", f.seq)
	secret := fmt.Sprintf("gs_tok_fake_%d", f.seq)
	exp := time.Now().Add(time.Duration(in.GetExpiresInSeconds()) * time.Second).UTC().Format(time.RFC3339)
	if f.expiresAtFn != nil {
		exp = f.expiresAtFn(in.GetExpiresInSeconds())
	}
	return &corev1.CreateApiKeyResponse{
		Key: &corev1.ApiKey{
			Id:             id,
			Name:           in.GetName(),
			Slices:         append([]string(nil), in.GetSlices()...),
			MaxRole:        in.GetMaxRole(),
			ExpiresAt:      exp,
			Purpose:        in.GetPurpose(),
			DaemonId:       in.GetDaemonId(),
			ConversationId: in.GetConversationId(),
		},
		Secret: secret,
	}, nil
}

func (f *fakeAgentAuthClient) RevokeApiKey(_ context.Context, in *corev1.RevokeApiKeyRequest, _ ...grpc.CallOption) (*corev1.RevokeApiKeyResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revokeErr != nil {
		return nil, f.revokeErr
	}
	f.revokeReqs = append(f.revokeReqs, in)
	return &corev1.RevokeApiKeyResponse{
		Key: &corev1.ApiKey{
			Id:        in.GetKeyId(),
			RevokedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

type fakeAgentRuntime struct {
	session         *fakeAgentSession
	openErr         error
	gotWorkdir      string
	gotResume       string
	gotInstructions string
	gotHistory      string
	gotEnv          map[string]string
	openCount       int
	skipHistory     bool
}

func (r *fakeAgentRuntime) OpenSession(ctx context.Context, workdir, resumeThreadID, instructions string, freshThreadHistory func(context.Context) (string, error)) (agentSession, error) {
	r.openCount++
	r.gotWorkdir = workdir
	r.gotResume = resumeThreadID
	r.gotInstructions = instructions
	r.gotEnv = agentRuntimeEnvFrom(ctx)
	if freshThreadHistory != nil && !r.skipHistory {
		if history, err := freshThreadHistory(ctx); err == nil {
			r.gotHistory = history
		}
	}
	if r.openErr != nil {
		return nil, r.openErr
	}
	if r.session == nil {
		r.session = &fakeAgentSession{}
	}
	return r.session, nil
}

type fakeAgentSession struct {
	emits     []agentRuntimeEvent
	threadID  string
	err       error
	gotPrompt string
	closed    int
	dead      bool
	runCount  int
}

func (s *fakeAgentSession) RunTurn(ctx context.Context, prompt string, emit func(agentRuntimeEvent)) error {
	s.runCount++
	s.gotPrompt = prompt
	for _, e := range s.emits {
		emit(e)
	}
	return s.err
}

func (s *fakeAgentSession) ThreadID() string { return s.threadID }

func (s *fakeAgentSession) Alive() bool { return !s.dead }

func (s *fakeAgentSession) Close() { s.closed++ }

func assertAgentEvent(t *testing.T, event *corev1.AgentEvent, conversationID, role, eventType, text string, final bool) {
	t.Helper()
	if event.GetConversationId() != conversationID {
		t.Fatalf("conversation_id = %q, want %q", event.GetConversationId(), conversationID)
	}
	if event.GetRole() != role {
		t.Fatalf("role = %q, want %q", event.GetRole(), role)
	}
	if event.GetType() != eventType {
		t.Fatalf("type = %q, want %q", event.GetType(), eventType)
	}
	if event.GetText() != text {
		t.Fatalf("text = %q, want %q", event.GetText(), text)
	}
	if event.GetFinal() != final {
		t.Fatalf("final = %t, want %t", event.GetFinal(), final)
	}
}
