package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

	// --role without --slice fails client-side cleanly.
	if _, _, err := run(human, "agent", "create", "bad-bot", "--role", "reader"); !isUserErrorCode(err, "slice_required") {
		t.Fatalf("role without slice err = %v, want slice_required", err)
	}

	// JSON output returns full metadata + one-time secret + slice/role.
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

	// Non-JSON prints secret cleanly on stdout and defaults --slice role to contributor.
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

	// GS_TOKEN + GS_SERVER_ADDR authenticate cleanly with an empty HOME containing no .gitslice/config.json.
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
		AgentService: &agentServiceProbes{
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
	d := &agentDaemon{allowedSlices: allowed}
	if !d.allowsSlice(&corev1.SliceRef{Account: "acme", Slice: "my-slice"}) {
		t.Fatal("expected normalized allowed slice to match")
	}
	if d.allowsSlice(&corev1.SliceRef{Account: "acme", Slice: "other"}) {
		t.Fatal("expected unlisted slice to be rejected")
	}
}

func TestDaemonChildKeyMintingRenewalIsolationAndRevocation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// Verify requireAgentWorkspaceDir allows both conversations/ and run-homes/.
	if err := os.MkdirAll(filepath.Join(root, "conversations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "run-homes"), 0o700); err != nil {
		t.Fatal(err)
	}
	origHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(origHome, ".gitslice"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origHome, ".gitslice", "config.json"), []byte(`{"token":"host-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(origHome, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", "")

	runner := Runner{Home: origHome, Dir: root}
	if err := runner.requireAgentWorkspaceDir(); err != nil {
		t.Fatalf("requireAgentWorkspaceDir with conversations and run-homes failed: %v", err)
	}

	auth := &fakeAgentAuthClient{}
	runtime := &fakeAgentRuntime{skipHistory: true}
	d := &agentDaemon{
		runner:        runner,
		cfg:           UserConfig{ServerAddr: "127.0.0.1:9090", Token: "daemon-root-token"},
		auth:          auth,
		runtime:       runtime,
		baseCtx:       ctx,
		workingDir:    root,
		daemonID:      "agent_daemon_1",
		allowedSlices: []string{"acme/app"},
		conversations: map[string]*agentConversation{},
	}

	// Out-of-scope slice is rejected by handleStartConversation without minting a key.
	d.handleStartConversation(ctx, &corev1.StartConversation{
		ConversationId: "conv-disallowed",
		Slice:          &corev1.SliceRef{Account: "acme", Slice: "secret"},
		Role:           "reader",
	})
	if len(auth.createReqs) != 0 {
		t.Fatalf("expected 0 CreateApiKey calls for disallowed slice, got %d", len(auth.createReqs))
	}

	// Fail-closed when CreateApiKey fails.
	auth.createErr = errors.New("mint refused")
	d.handleStartConversation(ctx, &corev1.StartConversation{
		ConversationId: "conv-fail",
		Slice:          &corev1.SliceRef{Account: "acme", Slice: "app"},
		Role:           "writer",
	})
	failConv := d.getConversation("conv-fail")
	if failConv == nil || failConv.waitReady(ctx) == nil {
		t.Fatalf("expected conv-fail to fail closed when child key minting fails")
	}
	auth.createErr = nil

	// Successful mint, isolated runHome without .gitslice/config.json, and CODEX_HOME preserved.
	conv, err := d.createConversation("conv-iso")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(conv.workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	conv.setConversationInfo("Fix bug", "acme", "app", "sl_1", "writer")
	conv.setReady(nil)
	runtime.session = &fakeAgentSession{threadID: "th_1", err: errors.New("stop after run")}

	d.handleUserMessage(ctx, &corev1.DeliverUserMessage{ConversationId: "conv-iso", Text: "hello", Seq: 1})
	if len(auth.createReqs) != 1 {
		t.Fatalf("CreateApiKey calls = %d, want 1", len(auth.createReqs))
	}
	req := auth.createReqs[0]
	if !slices.Equal(req.GetSlices(), []string{"acme/app"}) ||
		req.GetMaxRole() != "writer" ||
		req.GetExpiresInSeconds() != conversationChildKeyTTLSeconds ||
		req.GetPurpose() != "conversation conv-iso" ||
		req.GetDaemonId() != "agent_daemon_1" ||
		req.GetConversationId() != "conv-iso" {
		t.Fatalf("unexpected CreateApiKey request: %+v", req)
	}
	expectedRunHome := filepath.Join(root, "run-homes", "conv-iso")
	if runtime.gotEnv["HOME"] != expectedRunHome {
		t.Fatalf("runtime HOME = %q, want %q", runtime.gotEnv["HOME"], expectedRunHome)
	}
	if runtime.gotEnv["XDG_DATA_HOME"] != filepath.Join(expectedRunHome, ".local", "share") {
		t.Fatalf("runtime XDG_DATA_HOME = %q, want %q", runtime.gotEnv["XDG_DATA_HOME"], filepath.Join(expectedRunHome, ".local", "share"))
	}
	if runtime.gotEnv["GS_TOKEN"] != "gs_tok_fake_1" || runtime.gotEnv["GS_SERVER_ADDR"] != "127.0.0.1:9090" {
		t.Fatalf("unexpected runtime GS env: %+v", runtime.gotEnv)
	}
	if runtime.gotEnv["CODEX_HOME"] != filepath.Join(origHome, ".codex") {
		t.Fatalf("runtime CODEX_HOME = %q, want %q", runtime.gotEnv["CODEX_HOME"], filepath.Join(origHome, ".codex"))
	}
	if _, err := os.Stat(filepath.Join(expectedRunHome, ".gitslice", "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runHome must not contain .gitslice/config.json, stat err=%v", err)
	}

	// Metadata persists child_key_id and role, never the secret.
	d.persistConversationMeta(conv)
	metaBytes, err := os.ReadFile(filepath.Join(conv.workdir, agentMetaFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metaBytes), "gs_tok_fake_1") || !strings.Contains(string(metaBytes), "key_fake_1") || !strings.Contains(string(metaBytes), "writer") {
		t.Fatalf("unexpected persisted meta JSON:\n%s", string(metaBytes))
	}

	// Near-expiry key rotates cleanly, closing warm session and revoking previous key.
	warmSession := &fakeAgentSession{threadID: "th_1"}
	conv.stateMu.Lock()
	conv.session = warmSession
	conv.childExpiresAt = time.Now().Add(2 * time.Minute)
	conv.stateMu.Unlock()
	runtime.session = &fakeAgentSession{threadID: "th_2", err: errors.New("stop after rotate")}

	d.handleUserMessage(ctx, &corev1.DeliverUserMessage{ConversationId: "conv-iso", Text: "turn 2", Seq: 2})
	if len(auth.createReqs) != 2 || len(auth.revokeReqs) != 1 || auth.revokeReqs[0].GetKeyId() != "key_fake_1" {
		t.Fatalf("expected rotation create+revoke, creates=%d revokes=%+v", len(auth.createReqs), auth.revokeReqs)
	}
	if warmSession.closed != 1 {
		t.Fatalf("expected warm session to close on key rotation, closed=%d", warmSession.closed)
	}
	if runtime.gotEnv["GS_TOKEN"] != "gs_tok_fake_2" {
		t.Fatalf("rotated GS_TOKEN = %q, want gs_tok_fake_2", runtime.gotEnv["GS_TOKEN"])
	}

	// Closing workspace revokes current key and removes runHome.
	d.handleCloseWorkspace(&corev1.CloseWorkspace{ConversationId: "conv-iso", DeleteWorkspace: true})
	if len(auth.revokeReqs) != 2 || auth.revokeReqs[1].GetKeyId() != "key_fake_2" {
		t.Fatalf("expected key_fake_2 revoked on close, revokes=%+v", auth.revokeReqs)
	}
	if _, err := os.Stat(expectedRunHome); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected runHome removed on close, stat err=%v", err)
	}

	// ReconcileWorkspaces also revokes child key and removes runHome.
	conv2, err := d.createConversation("conv-stale")
	if err != nil {
		t.Fatal(err)
	}
	conv2.setConversationInfo("Stale", "acme", "app", "sl_1", "reader")
	conv2.setReady(nil)
	if _, err := d.ensureConversationChildKey(ctx, conv2); err != nil {
		t.Fatal(err)
	}
	staleHome := filepath.Join(root, "run-homes", "conv-stale")
	d.handleReconcileWorkspaces(&corev1.ReconcileWorkspaces{})
	if len(auth.revokeReqs) != 3 || auth.revokeReqs[2].GetKeyId() != "key_fake_3" {
		t.Fatalf("expected key_fake_3 revoked on reconcile, revokes=%+v", auth.revokeReqs)
	}
	if _, err := os.Stat(staleHome); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected staleHome removed on reconcile, stat err=%v", err)
	}
}

func TestHostCheckIsolatesHomeWithoutConfig(t *testing.T) {
	origHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(origHome, ".gitslice"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origHome, ".gitslice", "config.json"), []byte(`{"token":"host-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", origHome)

	repo := newFakeCheckRepo(map[string][]*corev1.TreeEntry{"/": nil}, nil)
	addr, stop := startFakeCheckRepoServer(t, repo)
	defer stop()

	sendQueue := &agentSendQueue{
		Ch:   make(chan *corev1.DaemonMessage, 16),
		Done: make(chan struct{}),
	}
	d := newTestCheckDaemon(t)
	d.cfg = UserConfig{ServerAddr: addr}
	d.baseCtx = context.Background()
	d.sendQueue = sendQueue

	req := &corev1.RunChecks{
		ResultTreeId: "tree-1",
		ServerAddr:   addr,
		Checks: []*corev1.CheckRunSpec{{
			RunId:            "run-isolated-home",
			Name:             "host-home",
			Command:          `test ! -e "$HOME/.gitslice/config.json" && printf 'isolated=%s' "$HOME"`,
			MaterializePaths: []string{"/"},
		}},
	}
	d.handleRunChecks(req)

	var logs strings.Builder
	var terminal *corev1.CheckRunUpdate
	for _, u := range collectCheckRunUpdates(sendQueue.Ch, "run-isolated-home") {
		logs.WriteString(u.GetLogChunk())
		if u.GetFinal() {
			terminal = u
		}
	}
	if terminal == nil {
		t.Fatal("missing terminal check update")
	}
	if terminal.GetStatus() != "passed" || terminal.GetExitCode() != 0 {
		t.Fatalf("check failed: status=%s exit=%d log=%q", terminal.GetStatus(), terminal.GetExitCode(), logs.String())
	}
	if !strings.Contains(logs.String(), "isolated=") || strings.Contains(logs.String(), origHome) {
		t.Fatalf("expected isolated HOME != %q, got logs %q", origHome, logs.String())
	}
}

func TestLazyWorkspaceIsolationReattachTeardownAndRotation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hostHome := t.TempDir()
	hostDataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", hostDataHome)

	workdir := filepath.Join(root, "conversations", "conv-lazy")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	runHome := filepath.Join(root, "run-homes", "conv-lazy")
	convRunner := Runner{Home: runHome}
	rec := mountRecord{
		ID:             "0123456789abcdef",
		Account:        "acme",
		Slice:          "app",
		MountPath:      workdir,
		ConversationID: "conv-lazy",
	}
	if err := os.MkdirAll(convRunner.mountStateDir(rec.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := convRunner.writeMountRecord(rec); err != nil {
		t.Fatal(err)
	}
	// Verify mountsRoot honors XDG_DATA_HOME inside child env (never leaking hostDataHome).
	isolatedRunner := Runner{}
	t.Setenv("XDG_DATA_HOME", filepath.Join(runHome, ".local", "share"))
	if got := isolatedRunner.mountsRoot(); got != filepath.Join(runHome, ".local", "share", "gitslice", "mounts") {
		t.Fatalf("isolated mountsRoot = %q, want under runHome", got)
	}
	t.Setenv("XDG_DATA_HOME", hostDataHome)

	if err := writeJSONFile(filepath.Join(workdir, agentMetaFilename), conversationMeta{
		ConversationID: "conv-lazy",
		Title:          "Lazy Conv",
		ThreadID:       "th_prev",
		SliceAccount:   "acme",
		SliceName:      "app",
		SliceID:        "sl_1",
		Role:           "contributor",
		ChildKeyID:     "key_prev",
	}, 0o600); err != nil {
		t.Fatal(err)
	}

	auth := &fakeAgentAuthClient{}
	mounted := false
	var gsCalls [][]string
	var gsEnvs []map[string]string
	var trace []string

	d := &agentDaemon{
		runner:        Runner{Home: hostHome},
		cfg:           UserConfig{ServerAddr: "127.0.0.1:9090", Token: "host-daemon-token"},
		auth:          auth,
		baseCtx:       ctx,
		workingDir:    root,
		daemonID:      "agent_daemon_lazy",
		conversations: map[string]*agentConversation{},
		isMountedFn: func(dir string) bool {
			return dir == workdir && mounted
		},
		runGSFn: func(_ context.Context, _ string, envOverrides map[string]string, args ...string) (string, error) {
			gsCalls = append(gsCalls, args)
			gsEnvs = append(gsEnvs, envOverrides)
			if slices.Contains(args, "mount") {
				trace = append(trace, "mount:"+envOverrides["GS_TOKEN"])
				mounted = true
			}
			return "", nil
		},
		unmountFn: func(_ context.Context, conversationID string, r mountRecord) error {
			// Verify runHome & mount.json are still intact when unmount runs!
			if _, err := os.Stat(filepath.Join(convRunner.mountStateDir(r.ID), "mount.json")); err != nil {
				t.Fatalf("mount.json missing during unmount for %s: %v", conversationID, err)
			}
			trace = append(trace, "unmount")
			mounted = false
			return nil
		},
	}

	// 1) reattachLazyWorkspaces fails closed when key minting errors (never touches host creds).
	auth.createErr = errors.New("mint refused")
	d.reattachLazyWorkspaces(ctx)
	if len(gsCalls) != 0 {
		t.Fatalf("reattachLazyWorkspaces must fail closed on CreateApiKey error, got calls: %v", gsCalls)
	}
	auth.createErr = nil

	// 2) reattachLazyWorkspaces mints child key and mounts with isolated HOME + XDG_DATA_HOME + GS_TOKEN.
	d.reattachLazyWorkspaces(ctx)
	if len(gsCalls) != 1 || !slices.Equal(gsCalls[0], []string{"--quiet", "mount", "--workspace", rec.ID, workdir}) {
		t.Fatalf("unexpected reattach gs calls: %v", gsCalls)
	}
	if gsEnvs[0]["HOME"] != runHome || gsEnvs[0]["XDG_DATA_HOME"] != filepath.Join(runHome, ".local", "share") || gsEnvs[0]["GS_TOKEN"] != "gs_tok_fake_1" {
		t.Fatalf("reattach env leaked or incomplete: %+v", gsEnvs[0])
	}
	if d.conversationWorkspaceUsable("conv-lazy", workdir) != true {
		t.Fatal("expected mounted lazy workspace under run-homes to be usable")
	}

	// 3) loadExistingConversations revokes previous daemon run's stale key and adopts reattached key.
	d.loadExistingConversations()
	if len(auth.revokeReqs) != 1 || auth.revokeReqs[0].GetKeyId() != "key_prev" {
		t.Fatalf("expected stale key_prev revoked on loadExistingConversations, revokes=%+v", auth.revokeReqs)
	}
	conv := d.getConversation("conv-lazy")
	if conv == nil || conv.getThreadID() != "th_prev" || conv.getChildKeyID() != "key_fake_1" {
		t.Fatalf("loaded conv missing preserved metadata or reattached key: %+v", conv)
	}

	// 4) Child-key rotation on mounted lazy workspace unmounts old mountd -> revokes old key -> remounts with new token.
	trace = nil
	conv.stateMu.Lock()
	conv.childExpiresAt = time.Now().Add(2 * time.Minute)
	conv.stateMu.Unlock()
	if _, err := d.ensureConversationChildKey(ctx, conv); err != nil {
		t.Fatalf("ensureConversationChildKey rotation: %v", err)
	}
	wantTrace := []string{"unmount", "mount:gs_tok_fake_2"}
	if !slices.Equal(trace, wantTrace) {
		t.Fatalf("rotation trace = %v, want %v", trace, wantTrace)
	}
	if len(auth.revokeReqs) != 2 || auth.revokeReqs[1].GetKeyId() != "key_fake_1" {
		t.Fatalf("expected key_fake_1 revoked during rotation, revokes=%+v", auth.revokeReqs)
	}
	if conv.getChildKeyID() != "key_fake_2" {
		t.Fatalf("rotated childKeyID = %q, want key_fake_2", conv.getChildKeyID())
	}

	// 5) handleCloseWorkspace with deleteWorkspace=false unmounts first, archives workdir, updates mount.json, revokes key, keeps runHome.
	d.handleCloseWorkspace(&corev1.CloseWorkspace{ConversationId: "conv-lazy", DeleteWorkspace: false})
	if mounted {
		t.Fatal("expected archive close to unmount lazy workspace")
	}
	archivedWorkdir := filepath.Join(root, "conversations", ".archived", "conv-lazy")
	if _, err := os.Stat(archivedWorkdir); err != nil {
		t.Fatalf("expected archived workdir at %s: %v", archivedWorkdir, err)
	}
	updatedRec, err := convRunner.readMountRecord(rec.ID)
	if err != nil || updatedRec.MountPath != archivedWorkdir {
		t.Fatalf("archived mount record = %+v, err=%v", updatedRec, err)
	}
	if len(auth.revokeReqs) != 3 || auth.revokeReqs[2].GetKeyId() != "key_fake_2" {
		t.Fatalf("expected key_fake_2 revoked on close, revokes=%+v", auth.revokeReqs)
	}

	// 6) handleCloseWorkspace and handleReconcileWorkspaces with deleteWorkspace=true unmount while mount.json exists, delete mount state + runHome, and revoke key.
	convDel, err := d.createConversation("conv-del")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(convDel.workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	convDel.setConversationInfo("Del", "acme", "app", "sl_1", "reader")
	convDel.setReady(nil)
	if _, err := d.ensureConversationChildKey(ctx, convDel); err != nil {
		t.Fatal(err)
	}
	delRunner := Runner{Home: filepath.Join(root, "run-homes", "conv-del")}
	delRec := mountRecord{ID: "fedcba9876543210", Account: "acme", Slice: "app", MountPath: convDel.workdir, ConversationID: "conv-del"}
	if err := os.MkdirAll(delRunner.mountStateDir(delRec.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := delRunner.writeMountRecord(delRec); err != nil {
		t.Fatal(err)
	}
	d.isMountedFn = func(dir string) bool {
		return dir == convDel.workdir
	}
	unmountChecked := false
	d.unmountFn = func(_ context.Context, conversationID string, r mountRecord) error {
		if _, err := os.Stat(filepath.Join(delRunner.mountStateDir(r.ID), "mount.json")); err != nil {
			t.Fatalf("mount.json missing during delete close unmount for %s: %v", conversationID, err)
		}
		unmountChecked = true
		return nil
	}
	d.handleCloseWorkspace(&corev1.CloseWorkspace{ConversationId: "conv-del", DeleteWorkspace: true})
	if !unmountChecked {
		t.Fatal("expected unmountFn to run on delete close")
	}
	if _, err := os.Stat(delRunner.Home); !os.IsNotExist(err) {
		t.Fatalf("expected runHome %s removed after delete close, err=%v", delRunner.Home, err)
	}
	if len(auth.revokeReqs) != 4 || auth.revokeReqs[3].GetKeyId() != "key_fake_3" {
		t.Fatalf("expected key_fake_3 revoked on delete close, revokes=%+v", auth.revokeReqs)
	}
}
