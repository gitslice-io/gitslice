package cli_test

import (
	"context"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestAgentKeepsPersonalAccountAndDaemonAfterJoiningOrg checks that an agent
// that joins an organization keeps its own account first and its daemon id.
// The daemon row is keyed by the caller's first account. When an org sorted
// ahead of the agent's personal account, a reconnecting CI daemon came back
// under a new id, and the slice's ci_daemon_id pointed at a daemon that never
// returned.
func TestAgentKeepsPersonalAccountAndDaemonAfterJoiningOrg(t *testing.T) {
	ts := startTestServer(t)
	operatorToken, _, operatorSubject := ts.provisionAccount(t, "zz-operator", "zz-operator")
	ts.agentSignup = true // applied by the restart in setOperators
	ts.setOperators(t, operatorSubject)

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	auth := corev1.NewAuthServiceClient(conn)
	agent, err := auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{
		Username:   "zz-ci-bot",
		OwnerEmail: "owner@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx := grpcAuthContext(agent.ApiKey)

	connectDaemon := func() string {
		t.Helper()
		ctx, cancel := context.WithTimeout(agentCtx, 30*time.Second)
		defer cancel()
		stream, err := corev1.NewAgentServiceClient(conn).Connect(ctx)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
			Name:    "ci",
			Runtime: "test",
			Version: "0.0.1",
		}}}); err != nil {
			t.Fatalf("send register: %v", err)
		}
		reg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv registered: %v", err)
		}
		return reg.GetRegistered().GetDaemonId()
	}
	before := connectDaemon()

	if _, err := auth.CreateOrganization(grpcAuthContext(operatorToken), &corev1.CreateOrganizationRequest{Slug: "aa-org"}); err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	operatorHome := t.TempDir()
	writeCLIAuthConfig(t, operatorHome, ts.addr, operatorToken, operatorSubject)
	runCLI(t, operatorHome, t.TempDir(), "account", "set-member", "aa-org", "zz-ci-bot", "--role", "reader")

	status, err := auth.GetAuthStatus(agentCtx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Accounts) != 2 || status.Accounts[0] != "zz-ci-bot" || status.Accounts[1] != "aa-org" {
		t.Fatalf("accounts = %v, want the agent's own account first", status.Accounts)
	}
	if after := connectDaemon(); after != before {
		t.Fatalf("daemon id changed after joining an org: %s -> %s", before, after)
	}
}
