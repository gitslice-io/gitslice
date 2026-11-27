package rpc_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/server"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testDispatchSecret = "test-dispatch-secret"

// fakeDispatcher stands in for the Cloudflare dispatcher: it checks the
// server's signature and hands the run ids it is posted to the test.
type fakeDispatcher struct {
	t    *testing.T
	runs chan string
}

func (d *fakeDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	mac := hmac.New(sha256.New, []byte(testDispatchSecret))
	mac.Write([]byte("dispatch"))
	mac.Write([]byte{0})
	mac.Write(body)
	if r.URL.Path != "/run" || !hmac.Equal([]byte(r.Header.Get("X-Gitslice-Dispatch-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var msg struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(body, &msg)
	d.runs <- msg.RunID
	w.WriteHeader(http.StatusAccepted)
}

type hookRequest struct {
	query     string
	signature string
	event     string
	trigger   string
	body      map[string]any
}

// TestRPCTriggers lands a triggers file and a script, then checks an http
// trigger fires on push with its secrets, and a worker trigger runs through
// the dispatcher API: it reports a required check, waits for a callback and
// gates submit until the check passes (design/34_triggers.md).
func TestRPCTriggers(t *testing.T) {
	var mu sync.Mutex
	var hooks []hookRequest
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		mu.Lock()
		hooks = append(hooks, hookRequest{query: r.URL.RawQuery, signature: r.Header.Get("X-Gitslice-Signature-256"), event: r.Header.Get("X-Gitslice-Event"), trigger: r.Header.Get("X-Gitslice-Trigger"), body: parsed})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	dispatcher := &fakeDispatcher{t: t, runs: make(chan string, 16)}
	dispatch := httptest.NewServer(dispatcher)
	defer dispatch.Close()

	ts := startRPCServerWith(t, func(cfg *server.Config) {
		cfg.Network.OutboundAllowPrivateTargets = true
		cfg.Integrations.TriggerDispatchURL = dispatch.URL
		cfg.Integrations.TriggerDispatchSecret = testDispatchSecret
	})
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	secrets := corev1.NewSecretServiceClient(conn)
	automationClient := corev1.NewAutomationServiceClient(conn)
	runClient := corev1.NewAutomationRunServiceClient(conn)
	checkClient := corev1.NewCheckServiceClient(conn)

	for name, value := range map[string]string{"HOOK_TOKEN": "hook-s3cret", "CI_TOKEN": "ci-s3cret"} {
		if _, err := secrets.CreateSecret(ctx, &corev1.CreateSecretRequest{Account: "acme", Name: name, Value: value, Scope: &corev1.SecretScope{Paths: []string{"/acme/payment"}}}); err != nil {
			t.Fatalf("CreateSecret %s: %v", name, err)
		}
	}
	submitDirectFile(t, ctx, clients, "/acme/payment/.gitslice/triggers/lib.js", "export const label = 'ci';\n", "trigger lib")
	submitDirectFile(t, ctx, clients, "/acme/payment/.gitslice/triggers/ci.js", "import { label } from './lib.js';\nexport default { async onEvent(event, gs) {} };\n", "trigger script")
	submitDirectFile(t, ctx, clients, "/acme/payment/.gitslice/automations.yaml", `
version: 1
automations:
  notify:
    on: [push]
    paths: ["**/*.go"]
    secrets: [HOOK_TOKEN]
    action:
      http:
        url: "`+receiver.URL+`/hook?t=${{ secrets.HOOK_TOKEN }}"
        sign: HOOK_TOKEN
  ci:
    on: [changeset.created, changeset.updated]
    paths: ["**/*.go"]
    checks: [ci]
    secrets: [CI_TOKEN]
    network: [ci.example.com]
    action:
      worker: triggers/ci.js
`, "triggers")

	listed, err := automationClient.ListAutomations(ctx, &corev1.ListAutomationsRequest{Path: "/acme/payment/gated"})
	if err != nil {
		t.Fatalf("ListAutomations: %v", err)
	}
	if len(listed.Automations) != 2 || listed.Automations[0].Id != "payment/ci" || listed.Automations[0].Checks[0] != "payment/ci" || listed.Automations[1].Action != "http" {
		t.Fatalf("ListAutomations = %v", listed)
	}

	// A slice that requires the trigger's check.
	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "gated", 0, []string{"payment/ci"})

	// A change that no trigger reports on: the required check is skipped.
	docID, _ := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/gated/README.md", "docs\n", "docs only")
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: docID}); err != nil {
		t.Fatalf("submit a change no trigger reports on: %v", err)
	}

	// A Go change: the worker trigger runs.
	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/gated/main.go", "package gated\n", "gated change")
	runID := waitForTriggerRun(t, ctx, automationClient, dispatcher, changesetID, "payment/ci")

	// The dispatcher's key is required, and the claim is exclusive.
	if _, err := runClient.StartRun(context.Background(), &corev1.StartAutomationRunRequest{RunId: runID, DispatcherKey: "wrong"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("StartRun with a wrong key = %v", err)
	}
	started, err := runClient.StartRun(context.Background(), &corev1.StartAutomationRunRequest{RunId: runID, DispatcherKey: testDispatchSecret})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := runClient.StartRun(context.Background(), &corev1.StartAutomationRunRequest{RunId: runID, DispatcherKey: testDispatchSecret}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second StartRun = %v, want FailedPrecondition", err)
	}
	if started.MainModule != "triggers/ci.js" || len(started.Modules) != 2 || started.Modules["triggers/lib.js"] == "" ||
		started.Secrets["CI_TOKEN"] != "ci-s3cret" || started.Network[0] != "ci.example.com" || started.CodeHash == "" {
		t.Fatalf("StartRun = %+v", started)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(started.EventJson), &event); err != nil {
		t.Fatal(err)
	}
	if event["trigger"].(map[string]any)["id"] != "payment/ci" || len(event["changes"].([]any)) != 1 {
		t.Fatalf("event = %v", event)
	}
	runToken := started.RunToken

	// The script reports a queued check, logs, keeps state and waits.
	check, err := runClient.CreateCheck(context.Background(), &corev1.CreateAutomationCheckRequest{RunToken: runToken, Name: "ci", Status: "running", DetailsUrl: "https://ci.example.com/build/1"})
	if err != nil {
		t.Fatalf("CreateCheck: %v", err)
	}
	if check.CheckName != "payment/ci" || check.Provenance != "trigger" || check.AutomationRunId != runID {
		t.Fatalf("CreateCheck = %v", check)
	}
	if _, err := runClient.CreateCheck(context.Background(), &corev1.CreateAutomationCheckRequest{RunToken: runToken, Name: "other"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("CreateCheck of an unlisted check = %v", err)
	}
	if _, err := runClient.AppendRunLog(context.Background(), &corev1.AppendAutomationRunLogRequest{RunToken: runToken, Lines: []*corev1.AutomationRunLogLine{{Text: "token ci-s3cret"}}}); err != nil {
		t.Fatalf("AppendRunLog: %v", err)
	}
	if _, err := runClient.SetRunState(context.Background(), &corev1.SetAutomationRunStateRequest{RunToken: runToken, StateJson: `{"build":1}`}); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}
	file, err := runClient.ReadFile(context.Background(), &corev1.AutomationReadFileRequest{RunToken: runToken, Path: "gated/main.go"})
	if err != nil || !file.Found || string(file.Content) != "package gated\n" {
		t.Fatalf("ReadFile = %v, %v", file, err)
	}
	if _, err := runClient.ReadFile(context.Background(), &corev1.AutomationReadFileRequest{RunToken: runToken, Path: "/other/x"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ReadFile outside the account = %v", err)
	}
	finished, err := runClient.FinishRun(context.Background(), &corev1.FinishAutomationRunRequest{RunToken: runToken})
	if err != nil || finished.Status != "waiting" {
		t.Fatalf("FinishRun with an open check = %v, %v; want waiting", finished, err)
	}

	// Submit waits for the check.
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: changesetID}); err == nil || !strings.Contains(err.Error(), "payment/ci") {
		t.Fatalf("submit before the check passed = %v", err)
	}

	// The callback: a forged signature is refused, the real one works.
	callback := started.CallbackUrl
	signature := callback[strings.LastIndex(callback, "/")+1:]
	if !strings.HasPrefix(callback, dispatch.URL+"/cb/"+runID+"/") {
		t.Fatalf("callback URL = %s", callback)
	}
	if _, err := runClient.StartCallback(context.Background(), &corev1.StartAutomationRunRequest{RunId: runID, DispatcherKey: testDispatchSecret, CallbackSignature: "forged"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("StartCallback with a forged signature = %v", err)
	}
	again, err := runClient.StartCallback(context.Background(), &corev1.StartAutomationRunRequest{RunId: runID, DispatcherKey: testDispatchSecret, CallbackSignature: signature})
	if err != nil || again.StateJson != `{"build": 1}` && again.StateJson != `{"build":1}` {
		t.Fatalf("StartCallback = %v, %v", again, err)
	}
	// Setting the status a check already has is harmless.
	if _, err := runClient.UpdateCheck(context.Background(), &corev1.UpdateAutomationCheckRequest{RunToken: again.RunToken, CheckRunId: check.Id, Status: "running"}); err != nil {
		t.Fatalf("repeated UpdateCheck: %v", err)
	}
	summary := "build 1 passed"
	passed, err := runClient.UpdateCheck(context.Background(), &corev1.UpdateAutomationCheckRequest{RunToken: again.RunToken, CheckRunId: check.Id, Status: "passed", Summary: &summary,
		Annotations: []*corev1.CheckAnnotation{{Path: "gated/main.go", StartLine: 1, Level: "notice", Message: "looks fine"}}})
	if err != nil {
		t.Fatalf("UpdateCheck: %v", err)
	}
	if passed.Status != "passed" || passed.DetailsUrl != "https://ci.example.com/build/1" || len(passed.Annotations) != 1 || passed.Annotations[0].Path != "/acme/payment/gated/main.go" {
		t.Fatalf("UpdateCheck = %v", passed)
	}
	// Its last check passed, so the run ended, and its token with it.
	if _, err := runClient.UpdateCheck(context.Background(), &corev1.UpdateAutomationCheckRequest{RunToken: again.RunToken, CheckRunId: check.Id, Status: "passed"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("UpdateCheck after the run ended = %v", err)
	}
	run, err := automationClient.GetAutomationRun(ctx, &corev1.GetAutomationRunRequest{RunId: runID})
	if err != nil || run.Status != "succeeded" {
		t.Fatalf("run after its check passed = %v, %v", run, err)
	}
	// A finished run's token is refused.
	if _, err := runClient.GetRunState(context.Background(), &corev1.AutomationRunTokenRequest{RunToken: runToken}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("GetRunState after the run finished = %v", err)
	}
	logs, err := automationClient.ListAutomationRunLogs(ctx, &corev1.ListAutomationRunLogsRequest{RunId: runID})
	if err != nil || len(logs.Logs) != 1 || logs.Logs[0].Chunk != "token ***\n" {
		t.Fatalf("logs = %v, %v", logs, err)
	}
	runs, err := checkClient.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: changesetID, PatchsetId: patchsetID})
	if err != nil || len(runs.Runs) != 1 {
		t.Fatalf("check runs = %v, %v", runs, err)
	}

	// Now it submits, and the push sends the http trigger.
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: changesetID}); err != nil {
		t.Fatalf("submit after the check passed: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var hook hookRequest
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, h := range hooks {
			if h.trigger == "payment/notify" {
				hook = h
			}
		}
		mu.Unlock()
		if hook.trigger != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if hook.trigger == "" {
		t.Fatal("the http trigger did not fire on push")
	}
	if hook.query != "t=hook-s3cret" || hook.event != "push" || !strings.HasPrefix(hook.signature, "sha256=") {
		t.Fatalf("hook = %+v", hook)
	}
	changes := hook.body["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["path"] != "/acme/payment/gated/main.go" {
		t.Fatalf("hook changes = %v", changes)
	}

	// A manual run needs the trigger to listen for manual.
	if _, err := automationClient.RunAutomation(ctx, &corev1.RunAutomationRequest{Account: "acme", AutomationId: "payment/ci"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("RunAutomation of a trigger without manual = %v", err)
	}
	// A rerun queues the same trigger for the same event.
	rerun, err := automationClient.RerunAutomationRun(ctx, &corev1.RerunAutomationRunRequest{RunId: runID})
	if err != nil || rerun.AutomationId != "payment/ci" || !strings.Contains(rerun.EventId, ".rerun.1") {
		t.Fatalf("RerunAutomationRun = %v, %v", rerun, err)
	}
	canceled, err := automationClient.CancelAutomationRun(ctx, &corev1.CancelAutomationRunRequest{RunId: rerun.Id})
	if err != nil || canceled.Status != "canceled" {
		t.Fatalf("CancelAutomationRun = %v, %v", canceled, err)
	}
}

// waitForTriggerRun waits until the dispatcher is posted a run of the
// trigger for the changeset, and returns its id.
func waitForTriggerRun(t *testing.T, ctx context.Context, client corev1.AutomationServiceClient, d *fakeDispatcher, changesetID, triggerID string) string {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case runID := <-d.runs:
			run, err := client.GetAutomationRun(ctx, &corev1.GetAutomationRunRequest{RunId: runID})
			if err != nil {
				t.Fatalf("GetAutomationRun %s: %v", runID, err)
			}
			if run.ChangesetId == changesetID && run.AutomationId == triggerID {
				return runID
			}
		case <-timeout:
			runs, _ := client.ListAutomationRuns(ctx, &corev1.ListAutomationRunsRequest{ChangesetId: changesetID})
			t.Fatalf("no run of %s for %s was posted; runs: %v", triggerID, changesetID, runs)
		}
	}
}
