package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/storage"
)

func TestClaimsListAndAccept(t *testing.T) {
	ctx := context.Background()
	const humanToken = "human-session-token"
	humanID := storage.ExternalSubjectID("owner")
	serverAddr, mem := startAgentSignupServerWithStores(t, true, map[string]string{humanToken: humanID})
	if _, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.SetVerifiedEmails(ctx, humanID, storage.ProviderService, []string{"owner@example.com"}); err != nil {
		t.Fatal(err)
	}

	var agentOut, stderr bytes.Buffer
	agentRunner := Runner{Home: t.TempDir(), Stdout: &agentOut, Stderr: &stderr}
	if err := agentRunner.Run(ctx, []string{"auth", "register-agent", "--server", serverAddr, "--username", "release-bot", "--email", "owner@example.com", "--json"}); err != nil {
		t.Fatalf("register-agent: %v\n%s", err, stderr.String())
	}
	var registered struct {
		SubjectID string `json:"subject_id"`
	}
	if err := json.Unmarshal(agentOut.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	human := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := human.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: humanToken, SubjectID: humanID}); err != nil {
		t.Fatal(err)
	}
	if err := human.Run(ctx, []string{"claims", "list", "--json"}); err != nil {
		t.Fatalf("claims list: %v\n%s", err, stderr.String())
	}
	var claims []pendingClaimOutput
	if err := json.Unmarshal(stdout.Bytes(), &claims); err != nil {
		t.Fatalf("claims list output: %v\n%s", err, stdout.String())
	}
	if len(claims) != 1 || claims[0].AgentSubjectID != registered.SubjectID || claims[0].Account != "release-bot" {
		t.Fatalf("claims = %+v", claims)
	}

	stdout.Reset()
	if err := human.Run(ctx, []string{"claims", "accept", registered.SubjectID}); err != nil {
		t.Fatalf("claims accept: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "co-own account release-bot") {
		t.Fatalf("accept output = %q", stdout.String())
	}
	if role, err := mem.Auth.AccountRole(ctx, humanID, "release-bot"); err != nil || role != "owner" {
		t.Fatalf("human role = %q, %v", role, err)
	}

	stderr.Reset()
	err := human.Run(ctx, []string{"claims", "accept", registered.SubjectID})
	if err == nil || !strings.Contains(err.Error(), "no pending claim") {
		t.Fatalf("second accept err = %v\n%s", err, stderr.String())
	}
}
