package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestEmbeddedSkillMatchesPublicSKILLMD(t *testing.T) {
	pubPath := filepath.Join("..", "..", "web", "public", "SKILL.md")
	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatalf("read %s: %v", pubPath, err)
	}
	if string(pubBytes) != embeddedSkillMarkdown {
		t.Fatalf("internal/cli/skill_embed.md is out of sync with web/public/SKILL.md; copy web/public/SKILL.md to internal/cli/skill_embed.md")
	}
	if !strings.HasPrefix(embeddedSkillMarkdown, "---\nname: gitslice\n") {
		t.Fatalf("SKILL.md missing YAML frontmatter with name: gitslice")
	}
}

func TestSkillInstallAndPrint(t *testing.T) {
	t.Run("print text and json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
		if err := r.Run(context.Background(), []string{"skill", "print"}); err != nil {
			t.Fatalf("skill print: %v\n%s", err, stderr.String())
		}
		if stdout.String() != embeddedSkillMarkdown {
			t.Fatalf("skill print output did not match embeddedSkillMarkdown")
		}

		stdout.Reset()
		if err := r.Run(context.Background(), []string{"skill", "print", "--json"}); err != nil {
			t.Fatalf("skill print --json: %v", err)
		}
		var payload struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if payload.Name != "gitslice" || payload.Content != embeddedSkillMarkdown {
			t.Fatalf("unexpected json payload: name=%q len=%d", payload.Name, len(payload.Content))
		}
	})

	t.Run("install auto detects existing harness dirs and falls back to .agents", func(t *testing.T) {
		home := t.TempDir()
		var stdout, stderr bytes.Buffer
		r := Runner{Home: home, Stdout: &stdout, Stderr: &stderr}
		if err := r.Run(context.Background(), []string{"skill", "install", "--json"}); err != nil {
			t.Fatalf("skill install fallback: %v\n%s", err, stderr.String())
		}
		var res skillInstallResult
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		wantFallback := filepath.Join(home, ".agents", "skills", "gitslice", "SKILL.md")
		if len(res.Installed) != 1 || res.Installed[0] != wantFallback {
			t.Fatalf("fallback installed = %v, want [%s]", res.Installed, wantFallback)
		}

		// Now add ~/.claude and ~/.gemini and re-run auto.
		for _, dir := range []string{".claude", ".gemini"} {
			if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		stdout.Reset()
		if err := r.Run(context.Background(), []string{"skill", "install", "--json"}); err != nil {
			t.Fatalf("skill install detected: %v", err)
		}
		res = skillInstallResult{}
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Installed) != 3 {
			t.Fatalf("expected 3 installed targets (.agents, .claude, .gemini), got %v", res.Installed)
		}
		for _, p := range res.Installed {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read installed skill %s: %v", p, err)
			}
			if string(data) != embeddedSkillMarkdown {
				t.Fatalf("installed skill content mismatch at %s", p)
			}
		}
	})

	t.Run("install project", func(t *testing.T) {
		projectDir := t.TempDir()
		var stdout, stderr bytes.Buffer
		r := Runner{Home: t.TempDir(), Dir: projectDir, Stdout: &stdout, Stderr: &stderr}
		if err := r.Run(context.Background(), []string{"skill", "install", "--project", "--json"}); err != nil {
			t.Fatalf("skill install --project: %v", err)
		}
		want := filepath.Join(projectDir, ".agents", "skills", "gitslice", "SKILL.md")
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("expected project skill at %s: %v", want, err)
		}
	})
}

func TestSchemaCommandFilterAndRemediationFields(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"schema", "auth", "join"}); err != nil {
		t.Fatalf("gs schema auth join: %v\n%s", err, stderr.String())
	}
	var doc parsedSchemaDoc
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Commands) != 1 || !strings.HasPrefix(doc.Commands[0].Use, "gs auth join") {
		t.Fatalf("filtered commands = %+v; want only gs auth join", doc.Commands)
	}
	shape, _ := doc.ErrorOutput["shape"].(map[string]any)
	errFields, _ := shape["error"].(map[string]any)
	if errFields["remediation_command"] == nil || errFields["remediation_url"] == nil {
		t.Fatalf("error_output missing remediation fields: %+v", errFields)
	}

	stdout.Reset()
	err := r.Run(context.Background(), []string{"schema", "definitely-not-a-command"})
	if err == nil || !isUserErrorCode(err, "not_found") {
		t.Fatalf("expected not_found error for unknown schema filter, got %v", err)
	}
}

func TestStructuredErrorRemediationFields(t *testing.T) {
	r := Runner{Home: t.TempDir()}

	unauthErr := r.unauthenticatedCommandError(grpcstatus.Error(codes.Unauthenticated, "unauthenticated"))
	classified := classifyError(unauthErr)
	if classified.Error.RemediationCommand == "" || classified.Error.RemediationURL == "" {
		t.Fatalf("unauthenticated error missing remediation: %+v", classified.Error)
	}

	permErr := r.permissionDeniedCommandError(grpcstatus.Error(codes.PermissionDenied, "permission denied"))
	classified = classifyError(permErr)
	if classified.Error.RemediationCommand != "gs doctor" || !strings.HasSuffix(classified.Error.RemediationURL, "/claims") {
		t.Fatalf("permission_denied remediation unexpected: %+v", classified.Error)
	}

	mcpErr := mcpToolError(userError("not_in_workspace", "not in a gitslice workspace", "Run gs init"))
	if mcpErr["remediation_command"] != "gs init <account/slice>" {
		t.Fatalf("mcpToolError remediation_command = %v", mcpErr["remediation_command"])
	}
}

func TestOnboardInvitesAndInitAgentGuideEndToEnd(t *testing.T) {
	ctx := context.Background()
	aliceTokens := map[string]string{}
	serverAddr, mem := startAgentSignupServerWithStores(t, true, aliceTokens)

	// Provision a human user "alice" with a verified email and a slice alice/app containing AGENTS.md.
	aliceSubjectID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "svc-alice", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.SetVerifiedEmails(ctx, aliceSubjectID, storage.ProviderService, []string{"alice@example.com"}); err != nil {
		t.Fatal(err)
	}
	aliceToken := "tok_alice_onboard_test"
	aliceTokens[aliceToken] = aliceSubjectID

	aliceHome := t.TempDir()
	aliceRunner := Runner{Home: aliceHome, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := aliceRunner.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      aliceToken,
		SubjectID:  aliceSubjectID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, aliceSubjectID, "alice"); err != nil {
		t.Fatalf("ChooseUsername: %v", err)
	}
	if _, err := mem.Slices.Create(ctx, aliceSubjectID, &corev1.SliceRef{Account: "alice", Slice: "app"}, []string{"/alice/app"}, "private", 0, nil); err != nil {
		t.Fatal(err)
	}

	// Seed an AGENTS.md file in /alice/app via alice's CLI runner and publish it deterministically.
	var seedOut bytes.Buffer
	aliceRunner.Stdout = &seedOut
	if err := aliceRunner.Run(ctx, []string{"fs", "write", "/alice/app/AGENTS.md", "--text", "# Agent Guide\nRun go test ./...\n", "--no-submit", "--json"}); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}
	var seedResp struct {
		ChangesetID string `json:"changeset_id"`
	}
	if err := json.Unmarshal(seedOut.Bytes(), &seedResp); err != nil || seedResp.ChangesetID == "" {
		t.Fatalf("unexpected seed response %q: %v", seedOut.String(), err)
	}
	if _, err := mem.Changesets.Submit(ctx, seedResp.ChangesetID, ""); err != nil {
		t.Fatalf("submit seed changeset: %v", err)
	}
	if _, err := mem.Changesets.PublishPending(ctx, 10); err != nil {
		t.Fatalf("publish seed changeset: %v", err)
	}

	// 1. Human creates a single-use invite for alice/app.
	var aliceOut, aliceErr bytes.Buffer
	aliceRunner.Stdout = &aliceOut
	aliceRunner.Stderr = &aliceErr
	if err := aliceRunner.Run(ctx, []string{"auth", "create-agent-invite", "--slice", "alice/app", "--role", "writer", "--username", "deploy-bot", "--json"}); err != nil {
		t.Fatalf("create-agent-invite: %v\n%s", err, aliceErr.String())
	}
	var invOut createAgentInviteOutput
	if err := json.Unmarshal(aliceOut.Bytes(), &invOut); err != nil {
		t.Fatalf("unmarshal invite: %v", err)
	}
	if !strings.HasPrefix(invOut.Token, "gsi_") || invOut.Slice != "alice/app" || invOut.Role != "writer" {
		t.Fatalf("unexpected invite output: %+v", invOut)
	}

	// 2. Agent redeems invite with one-shot gs onboard --join --install-skill --dir.
	agentHome := t.TempDir()
	wsDir := filepath.Join(t.TempDir(), "ws")
	var agentOut, agentErr bytes.Buffer
	agentRunner := Runner{Home: agentHome, Stdout: &agentOut, Stderr: &agentErr}
	if err := agentRunner.Run(ctx, []string{
		"onboard",
		"--server", serverAddr,
		"--join", invOut.Token,
		"--install-skill",
		"--dir", wsDir,
		"--json",
	}); err != nil {
		t.Fatalf("onboard --join: %v\n%s", err, agentErr.String())
	}
	var onboardRes onboardOutput
	if err := json.Unmarshal(agentOut.Bytes(), &onboardRes); err != nil {
		t.Fatalf("unmarshal onboard: %v", err)
	}
	if onboardRes.Status != "joined" || onboardRes.Account != "deploy-bot" || onboardRes.WorkspaceDir != wsDir || len(onboardRes.SkillInstalled) == 0 {
		t.Fatalf("unexpected onboard output: %+v", onboardRes)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "alice", "app", "AGENTS.md")); err != nil {
		t.Fatalf("expected AGENTS.md checked out in workspace: %v", err)
	}

	// 3. Running gs onboard again as the same agent is idempotent.
	agentOut.Reset()
	agentErr.Reset()
	if err := agentRunner.Run(ctx, []string{"onboard", "--server", serverAddr, "--slice", "alice/app", "--json"}); err != nil {
		t.Fatalf("idempotent onboard: %v\n%s", err, agentErr.String())
	}
	onboardRes = onboardOutput{}
	if err := json.Unmarshal(agentOut.Bytes(), &onboardRes); err != nil {
		t.Fatal(err)
	}
	if !onboardRes.AlreadyAuthenticated || onboardRes.Status != "already_authenticated" || onboardRes.SliceAccess != "accessible" {
		t.Fatalf("expected idempotent already_authenticated + accessible, got %+v", onboardRes)
	}

	// 4. Direct gs init --json discovers AGENTS.md in agent_guides.
	initDir := t.TempDir()
	agentOut.Reset()
	agentErr.Reset()
	agentRunner.Dir = initDir
	if err := agentRunner.Run(ctx, []string{"init", "alice/app", "--json"}); err != nil {
		t.Fatalf("gs init --json: %v\n%s", err, agentErr.String())
	}
	var initRes struct {
		SliceRef    string   `json:"slice_ref"`
		AgentGuides []string `json:"agent_guides"`
	}
	if err := json.Unmarshal(agentOut.Bytes(), &initRes); err != nil {
		t.Fatalf("unmarshal init: %v", err)
	}
	if initRes.SliceRef != "alice/app" || len(initRes.AgentGuides) != 1 || !strings.HasSuffix(initRes.AgentGuides[0], "AGENTS.md") {
		t.Fatalf("unexpected initRes: %+v", initRes)
	}

	// 5. Self-registration via gs onboard --email --slice + one-click gs claims accept grants the slice.
	agent2Home := t.TempDir()
	var agent2Out, agent2Err bytes.Buffer
	agent2Runner := Runner{Home: agent2Home, Stdout: &agent2Out, Stderr: &agent2Err}
	if err := agent2Runner.Run(ctx, []string{
		"onboard",
		"--server", serverAddr,
		"--web-url", "https://gitslice.test",
		"--email", "alice@example.com",
		"--slice", "alice/app",
		"--role", "contributor",
		"--json",
	}); err != nil {
		t.Fatalf("onboard --email: %v\n%s", err, agent2Err.String())
	}
	var regOnboard onboardOutput
	if err := json.Unmarshal(agent2Out.Bytes(), &regOnboard); err != nil {
		t.Fatal(err)
	}
	if regOnboard.Status != "registered" || regOnboard.RequestedSlice != "alice/app" || regOnboard.RequestedRole != "contributor" || regOnboard.SliceAccess != "pending_access" {
		t.Fatalf("unexpected self-reg onboard: %+v", regOnboard)
	}

	// Alice accepts claim via CLI; --grant-slice defaults to true.
	aliceOut.Reset()
	aliceErr.Reset()
	if err := aliceRunner.Run(ctx, []string{"claims", "accept", regOnboard.SubjectID, "--json"}); err != nil {
		t.Fatalf("claims accept: %v\n%s", err, aliceErr.String())
	}
	var acceptRes struct {
		Account      string `json:"account"`
		GrantedSlice string `json:"granted_slice"`
		GrantedRole  string `json:"granted_role"`
	}
	if err := json.Unmarshal(aliceOut.Bytes(), &acceptRes); err != nil {
		t.Fatal(err)
	}
	if acceptRes.GrantedSlice != "alice/app" || acceptRes.GrantedRole != "contributor" {
		t.Fatalf("expected alice/app (contributor) granted on claim accept, got %+v", acceptRes)
	}
}
