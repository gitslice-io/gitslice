package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/agentd"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestAgentCreateCLICreatesDelegatedAgentAndEnvTokenWorksWithoutConfig(t *testing.T) {
	ctx := context.Background()
	const humanToken = "human-session-token"
	humanID := storage.ExternalSubjectID("owner")
	serverAddr, mem := startAgentSignupServerWithStores(t, true, map[string]string{humanToken: humanID})
	if _, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, humanID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.Create(ctx, humanID, &corev1.SliceRef{Account: "owner", Slice: "app"}, []string{"/owner/app"}, "private", 0, nil); err != nil {
		t.Fatal(err)
	}

	run := func(r Runner, args ...string) (string, string, error) {
		var stdout, stderr bytes.Buffer
		r.Stdout, r.Stderr = &stdout, &stderr
		err := r.Run(ctx, args)
		return stdout.String(), stderr.String(), err
	}

	human := Runner{Home: t.TempDir()}
	if err := human.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: humanToken, SubjectID: humanID}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(human, "agent", "create", "bad-bot", "--role", "reader"); !isUserErrorCode(err, "slice_required") {
		t.Fatalf("role without slice err = %v, want slice_required", err)
	}

	stdout, _, err := run(human, "agent", "create", "deploy-bot", "--slice", "owner/app", "--role", "reader", "--display-name", "Deploy Bot", "--json")
	if err != nil {
		t.Fatalf("agent create --json: %v", err)
	}
	var created struct {
		AgentSubjectID string       `json:"agent_subject_id"`
		Account        string       `json:"account"`
		Key            apiKeyOutput `json:"key"`
		Secret         string       `json:"secret"`
		Slice          string       `json:"slice"`
		Role           string       `json:"role"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("unmarshal agent create JSON: %v\n%s", err, stdout)
	}
	if created.Account != "deploy-bot" || created.Slice != "owner:app" || created.Role != "reader" ||
		created.Key.ID == "" || !strings.HasPrefix(created.Secret, storage.APIKeyPrefix) {
		t.Fatalf("unexpected agent create output: %+v", created)
	}

	plainOut, plainErr, err := run(human, "agent", "create", "contrib-bot", "--slice", "owner/app")
	if err != nil {
		t.Fatalf("agent create plain: %v\n%s", err, plainErr)
	}
	plainSecret := strings.TrimSpace(plainOut)
	if !strings.HasPrefix(plainSecret, storage.APIKeyPrefix) || strings.ContainsAny(plainSecret, " \n") {
		t.Fatalf("plain stdout = %q, want bare API key secret", plainOut)
	}
	if !strings.Contains(plainErr, "Added to owner:app as contributor.") {
		t.Fatalf("stderr = %q, want default contributor message", plainErr)
	}

	emptyHome := t.TempDir()
	t.Setenv("GS_TOKEN", created.Secret)
	t.Setenv("GS_SERVER_ADDR", serverAddr)
	agentRunner := Runner{Home: emptyHome}
	statusOut, statusErr, err := run(agentRunner, "auth", "status", "--json")
	if err != nil {
		t.Fatalf("auth status with GS_TOKEN and no config.json: %v\n%s", err, statusErr)
	}
	var st authStatusOutput
	if err := json.Unmarshal([]byte(statusOut), &st); err != nil {
		t.Fatalf("unmarshal auth status: %v\n%s", err, statusOut)
	}
	if !st.SignedIn || st.SubjectID != created.AgentSubjectID {
		t.Fatalf("unexpected auth status with GS_TOKEN: %+v", st)
	}
}

func TestAgentStartAndInstallSliceFlags(t *testing.T) {
	ctx := context.Background()
	r := Runner{Home: t.TempDir(), Dir: t.TempDir()}

	if err := r.Run(ctx, []string{"agent", "start", "--ci", "--slice", "acme/app"}); !isUserErrorCode(err, "invalid_args") {
		t.Fatalf("start --ci --slice err = %v, want invalid_args", err)
	}
	if err := r.Run(ctx, []string{"agent", "start", "--slice", "not-a-ref"}); !isUserErrorCode(err, "invalid_slice_ref") {
		t.Fatalf("start invalid slice err = %v, want invalid_slice_ref", err)
	}
	if err := r.Run(ctx, []string{"agent", "install", "--ci", "--slice", "acme/app", "--print"}); !isUserErrorCode(err, "invalid_args") {
		t.Fatalf("install --ci --slice err = %v, want invalid_args", err)
	}

	var out bytes.Buffer
	ir := Runner{
		Home:   t.TempDir(),
		Dir:    t.TempDir(),
		Stdout: &out,
		AgentService: &agentd.ServiceProbes{
			OSName:     "darwin",
			Executable: func() (string, error) { return "/usr/local/bin/gs", nil },
			UserHome:   func() (string, error) { return t.TempDir(), nil },
			Environ:    func() []string { return nil },
		},
	}
	if err := ir.Run(ctx, []string{"agent", "install", "--name", "slice-bot", "--slice", "Acme/Payment_API", "--slice", "acme/docs", "--slice", "acme/docs", "--print"}); err != nil {
		t.Fatalf("agent install --slice --print: %v", err)
	}
	printed := out.String()
	for _, want := range []string{"<string>--slice</string>", "<string>acme/payment-api</string>", "<string>acme/docs</string>"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("printed plist missing %q:\n%s", want, printed)
		}
	}

	allowed, err := normalizeDaemonAllowedSlices([]string{"Acme:My_Slice"})
	if err != nil {
		t.Fatal(err)
	}
	d := &agentd.Daemon{AllowedSlices: allowed}
	if !d.AllowsSlice(&corev1.SliceRef{Account: "acme", Slice: "my-slice"}) {
		t.Fatal("expected normalized allowed slice to match")
	}
	if d.AllowsSlice(&corev1.SliceRef{Account: "acme", Slice: "other"}) {
		t.Fatal("expected unlisted slice to be rejected")
	}
}
