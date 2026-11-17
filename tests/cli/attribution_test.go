package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestAuditAttributionCLIShowAndControllerSubmitEndToEnd(t *testing.T) {
	ts := startTestServer(t)

	nicHome := t.TempDir()
	nicWS := t.TempDir()
	loginTestCLI(t, ts, nicHome, nicWS)

	// Provision a delegated agent controlled by the signed-in owner on acme:payment.
	agentJSON := runCLI(t, nicHome, nicWS, "agent", "create", "web-agent", "--slice", "acme/payment", "--role", "contributor", "--json")
	var createdAgent struct {
		AgentSubjectID string `json:"agent_subject_id"`
		Account        string `json:"account"`
		Secret         string `json:"secret"`
		Key            struct {
			ID string `json:"id"`
		} `json:"key"`
	}
	if err := json.Unmarshal([]byte(agentJSON), &createdAgent); err != nil {
		t.Fatalf("unmarshal agent create: %v\n%s", err, agentJSON)
	}

	ownerCfgJSON := runCLI(t, nicHome, nicWS, "context", "--json")
	var ownerCtx struct {
		Account string `json:"account"`
	}
	_ = json.Unmarshal([]byte(ownerCfgJSON), &ownerCtx)

	// Mint a child API key carrying daemon_id on the agent's key, and initialize agent workspace.
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	authClient := corev1.NewAuthServiceClient(conn)

	agentHome := t.TempDir()
	agentWS := t.TempDir()
	writeCLIAuthConfig(t, agentHome, ts.addr, createdAgent.Secret, createdAgent.AgentSubjectID)
	runCLI(t, agentHome, agentWS, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, agentWS, "acme/payment/feature.go", "package payment\nconst Feature = 1\n")

	createOut := runCLI(t, agentHome, agentWS, "create", "--all", "--message", "agent feature", "--json")
	var createdCS struct {
		ChangesetID string `json:"changeset_id"`
	}
	if err := json.Unmarshal([]byte(createOut), &createdCS); err != nil {
		t.Fatalf("unmarshal create: %v\n%s", err, createOut)
	}

	// Owner submits the agent's changeset via controller delegation!
	runCLI(t, nicHome, nicWS, "submit", createdCS.ChangesetID)

	// gs show <changeset> displays "web-agent (for <owner>)" for author and <owner> for submitted_by!
	showText := runCLI(t, nicHome, nicWS, "show", createdCS.ChangesetID)
	if !strings.Contains(showText, "author: web-agent (for ") {
		t.Fatalf("expected 'author: web-agent (for ...)' in gs show output, got:\n%s", showText)
	}
	if !strings.Contains(showText, "submitted_by: ") || strings.Contains(showText, "submitted_by: web-agent") {
		t.Fatalf("expected human controller in submitted_by (not web-agent), got:\n%s", showText)
	}

	// Verify JSON carries structured attribution & submitted_attribution with key_id!
	showJSON := runCLI(t, nicHome, nicWS, "cs", "show", createdCS.ChangesetID, "--json")
	var csProto corev1.Changeset
	if err := json.Unmarshal([]byte(showJSON), &csProto); err != nil {
		t.Fatalf("unmarshal cs show --json: %v\n%s", err, showJSON)
	}
	if csProto.GetAttribution().GetActingUsername() != "web-agent" || csProto.GetAttribution().GetKeyId() != createdAgent.Key.ID {
		t.Fatalf("unexpected JSON attribution: %+v", csProto.GetAttribution())
	}
	if csProto.GetSubmittedAttribution().GetActingSubjectId() == "" || csProto.GetSubmittedAttribution().GetActingUsername() == "web-agent" {
		t.Fatalf("expected human controller on submitted_attribution, got: %+v", csProto.GetSubmittedAttribution())
	}
	_ = authClient
}
