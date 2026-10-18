package rpc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/auth/servicetoken"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// TestDaemonsShowToTheAgentsOwnersAndCanBeRenamed follows a daemon an agent
// started: invisible to a stranger, listed for the human who claims the agent,
// renamed by that human without losing the name it registers with, and shown by
// its new name wherever a slice points at it.
func TestDaemonsShowToTheAgentsOwnersAndCanBeRenamed(t *testing.T) {
	ts := startRPCServer(t)
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	auth := corev1.NewAuthServiceClient(conn)
	agents := corev1.NewAgentServiceClient(conn)
	slices := corev1.NewSliceServiceClient(conn)

	agent, err := auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{Username: "ci-bot", OwnerEmail: "ci-owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx := grpcAuthContext(agent.ApiKey)
	mint := func(subject, email string, verified bool) context.Context {
		t.Helper()
		mintToken := servicetoken.Mint
		if verified {
			mintToken = servicetoken.MintVerifiedEmail
		}
		token, err := mintToken(ts.servicePriv, subject, email, servicetoken.DefaultIssuer, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return grpcAuthContext(token)
	}

	// The agent starts two daemons, as gs agent start does.
	register := func(name string) string {
		t.Helper()
		ctx, cancel := context.WithCancel(agentCtx)
		t.Cleanup(cancel)
		stream, err := agents.Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{Name: name, Runtime: "codex", Version: "0.1"}}}); err != nil {
			t.Fatal(err)
		}
		reg, err := stream.Recv()
		if err != nil || reg.GetRegistered().GetDaemonId() == "" {
			t.Fatalf("register %s: %v, %v", name, reg, err)
		}
		return reg.GetRegistered().GetDaemonId()
	}
	ciID := register("ci-bot-ci")
	otherID := register("scratch")

	list := func(ctx context.Context) []*corev1.AgentDaemon {
		t.Helper()
		res, err := agents.ListDaemons(ctx, &corev1.ListDaemonsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return res.Daemons
	}
	find := func(daemons []*corev1.AgentDaemon, id string) *corev1.AgentDaemon {
		for _, d := range daemons {
			if d.Id == id {
				return d
			}
		}
		return nil
	}

	// The agent sees its own daemons, marked as its own.
	if d := find(list(agentCtx), ciID); d == nil || !d.Mine || d.Name != "ci-bot-ci" || d.DisplayName != "" || d.Status != "online" {
		t.Fatalf("the agent's view of its daemon = %+v", d)
	}

	// A stranger sees nothing, and cannot rename what they cannot see.
	stranger := mint("svc_daemon_stranger", "stranger@example.com", true)
	if got := list(stranger); len(got) != 0 {
		t.Fatalf("a stranger lists %d daemons", len(got))
	}
	if _, err := agents.RenameDaemon(stranger, &corev1.RenameDaemonRequest{DaemonId: ciID, DisplayName: "mine now"}); grpcstatus.Code(err) != codes.NotFound {
		t.Fatalf("a stranger's rename code = %v; want NotFound", grpcstatus.Code(err))
	}

	// The agent's owner sees the daemons once they claim the agent.
	owner := mint("svc_daemon_owner", "ci-owner@example.com", true)
	if got := list(owner); len(got) != 0 {
		t.Fatalf("an owner who has not claimed the agent lists %d daemons", len(got))
	}
	if _, err := auth.AcceptClaim(owner, &corev1.AcceptClaimRequest{AgentSubjectId: agent.SubjectId}); err != nil {
		t.Fatal(err)
	}
	seen := list(owner)
	if d := find(seen, ciID); d == nil || d.Mine || d.Name != "ci-bot-ci" || d.Account != "ci-bot" {
		t.Fatalf("the owner's view of the agent's daemon = %+v", d)
	}
	if len(seen) != 2 {
		t.Fatalf("the owner lists %d daemons, want the agent's two", len(seen))
	}

	// What the owner sees, they do not run conversations on: those stay with
	// the identity that registered the daemon.
	if _, err := agents.CreateConversation(owner, &corev1.CreateConversationRequest{DaemonId: ciID, Slice: &corev1.SliceRef{Account: "ci-bot", Slice: "home"}, Title: "not yours"}); grpcstatus.Code(err) != codes.NotFound {
		t.Fatalf("a conversation on the agent's daemon code = %v; want NotFound", grpcstatus.Code(err))
	}

	// Renaming.
	renamed, err := agents.RenameDaemon(owner, &corev1.RenameDaemonRequest{DaemonId: ciID, DisplayName: "  Main CI  "})
	if err != nil || renamed.DisplayName != "Main CI" || renamed.Name != "ci-bot-ci" || renamed.Mine {
		t.Fatalf("RenameDaemon = %+v, %v", renamed, err)
	}
	// Another daemon of the account cannot take the same name, in any case.
	if _, err := agents.RenameDaemon(agentCtx, &corev1.RenameDaemonRequest{DaemonId: otherID, DisplayName: "main ci"}); grpcstatus.Code(err) != codes.AlreadyExists {
		t.Fatalf("a taken name code = %v; want AlreadyExists", grpcstatus.Code(err))
	}
	// Nor the name another daemon registered with.
	if _, err := agents.RenameDaemon(owner, &corev1.RenameDaemonRequest{DaemonId: ciID, DisplayName: "Scratch"}); grpcstatus.Code(err) != codes.AlreadyExists {
		t.Fatalf("a registered name code = %v; want AlreadyExists", grpcstatus.Code(err))
	}
	for name, want := range map[string]string{strings.Repeat("x", 65): "at most 64", "two\nlines": "control character"} {
		if _, err := agents.RenameDaemon(owner, &corev1.RenameDaemonRequest{DaemonId: ciID, DisplayName: name}); grpcstatus.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), want) {
			t.Fatalf("rename to %q: %v; want InvalidArgument (%s)", name, err, want)
		}
	}
	// The agent that registered the daemon can rename it too.
	if d, err := agents.RenameDaemon(agentCtx, &corev1.RenameDaemonRequest{DaemonId: otherID, DisplayName: "Scratch box"}); err != nil || d.DisplayName != "Scratch box" || !d.Mine {
		t.Fatalf("the agent's own rename = %+v, %v", d, err)
	}

	// A restart registers under the same name and finds the same daemon, with
	// the name it was given.
	if again := register("ci-bot-ci"); again != ciID {
		t.Fatalf("a restarted daemon got a new id %s, want %s", again, ciID)
	}
	if d := find(list(owner), ciID); d == nil || d.DisplayName != "Main CI" {
		t.Fatalf("after a restart the daemon is shown as %+v", d)
	}

	// A slice that uses the daemon shows its name, and the daemon lists the slice.
	if _, err := slices.SetSliceCIDaemon(owner, &corev1.SetSliceCIDaemonRequest{Slice: &corev1.SliceRef{Account: "ci-bot", Slice: "home"}, DaemonId: ciID}); err != nil {
		t.Fatal(err)
	}
	slice, err := slices.ResolveSlice(owner, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "ci-bot", Slice: "home"}})
	if err != nil || slice.CiDaemonId != ciID || slice.CiDaemonName != "Main CI" {
		t.Fatalf("ResolveSlice = %+v, %v; want the CI daemon shown as Main CI", slice, err)
	}
	if d := find(list(owner), ciID); d == nil || len(d.CiSlices) != 1 || d.CiSlices[0].Account != "ci-bot" || d.CiSlices[0].Slice != "home" {
		t.Fatalf("the daemon's slices = %+v", d)
	}

	// Someone with a slice of their own cannot send its checks, and its
	// secrets, to a daemon that is not theirs, whatever its name or id.
	mallory := grpcAuthContext(ts.loginViaGRPC(t, "mallory"))
	malloryHome := &corev1.SliceRef{Account: "mallory", Slice: "home"}
	if _, err := slices.SetSliceCIDaemon(mallory, &corev1.SetSliceCIDaemonRequest{Slice: malloryHome, DaemonId: ciID}); grpcstatus.Code(err) != codes.NotFound {
		t.Fatalf("naming another person's daemon code = %v; want NotFound", grpcstatus.Code(err))
	}
	if got, err := slices.ResolveSlice(mallory, &corev1.ResolveSliceRequest{Ref: malloryHome}); err != nil || got.CiDaemonId != "" {
		t.Fatalf("after the refused request, mallory/home = %+v, %v", got, err)
	}
	// The agent itself, which registered the daemon, may name it for its own slice.
	if _, err := slices.SetSliceCIDaemon(agentCtx, &corev1.SetSliceCIDaemonRequest{Slice: &corev1.SliceRef{Account: "ci-bot", Slice: "home"}, DaemonId: otherID}); err != nil {
		t.Fatalf("the agent naming its own daemon: %v", err)
	}

	// Clearing the name shows the registered one again.
	if _, err := slices.SetSliceCIDaemon(owner, &corev1.SetSliceCIDaemonRequest{Slice: &corev1.SliceRef{Account: "ci-bot", Slice: "home"}, DaemonId: ciID}); err != nil {
		t.Fatal(err)
	}
	cleared, err := agents.RenameDaemon(owner, &corev1.RenameDaemonRequest{DaemonId: ciID})
	if err != nil || cleared.DisplayName != "" {
		t.Fatalf("clearing the name = %+v, %v", cleared, err)
	}
	if slice, err := slices.ResolveSlice(owner, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "ci-bot", Slice: "home"}}); err != nil || slice.CiDaemonName != "ci-bot-ci" {
		t.Fatalf("after clearing, the CI daemon is shown as %+v, %v", slice, err)
	}
}
