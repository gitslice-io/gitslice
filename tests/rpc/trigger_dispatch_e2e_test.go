package rpc_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/server"
)

// TestRPCTriggerDispatchE2E runs a worker trigger through the real
// dispatcher (trigger-dispatch/, under wrangler dev) as a Dynamic Worker: the
// script reports a check, calls an allowed host with a secret, is refused
// another host, waits, and finishes on a callback. It needs npm, the
// dispatcher's node_modules and the internet (httpbin.org), so it runs only
// with GITSLICE_TRIGGER_E2E=1.
func TestRPCTriggerDispatchE2E(t *testing.T) {
	if os.Getenv("GITSLICE_TRIGGER_E2E") != "1" {
		t.Skip("set GITSLICE_TRIGGER_E2E=1 to run the dispatcher end to end")
	}
	_, file, _, _ := runtime.Caller(0)
	dispatchDir := filepath.Join(filepath.Dir(file), "..", "..", "trigger-dispatch")
	dispatchAddr := freeAddr(t)
	dispatchURL := "http://" + dispatchAddr
	const secret = "e2e-dispatch-secret"

	ts := startRPCServerWith(t, func(cfg *server.Config) {
		cfg.TriggerDispatchURL = dispatchURL
		cfg.TriggerDispatchSecret = secret
	})
	port := dispatchAddr[strings.LastIndex(dispatchAddr, ":")+1:]
	wrangler := exec.Command("npx", "wrangler", "dev", "--port", port, "--ip", "127.0.0.1",
		"--var", "DISPATCH_SECRET:"+secret, "--var", "GITSLICE_API:http://"+ts.addr)
	wrangler.Dir = dispatchDir
	wrangler.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logFile, err := os.CreateTemp(t.TempDir(), "wrangler-*.log")
	if err != nil {
		t.Fatal(err)
	}
	wrangler.Stdout, wrangler.Stderr = logFile, logFile
	if err := wrangler.Start(); err != nil {
		t.Fatalf("start wrangler dev: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-wrangler.Process.Pid, syscall.SIGTERM)
		_ = wrangler.Wait()
		if t.Failed() {
			data, _ := os.ReadFile(logFile.Name())
			t.Logf("wrangler dev log:\n%s", data)
		}
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(dispatchURL + "/")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wrangler dev did not start")
		}
		time.Sleep(500 * time.Millisecond)
	}

	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	triggerClient := corev1.NewTriggerServiceClient(conn)
	checkClient := corev1.NewCheckServiceClient(conn)
	if _, err := corev1.NewSecretServiceClient(conn).CreateSecret(ctx, &corev1.CreateSecretRequest{
		Account: "acme", Name: "CI_TOKEN", Value: "e2e-s3cret-value", Scope: &corev1.SecretScope{Paths: []string{"/acme/payment"}},
	}); err != nil {
		t.Fatal(err)
	}
	submitDirectFile(t, ctx, clients, "/acme/payment/.gitslice/triggers/lib.js", "export const label = 'from-lib';\n", "lib")
	submitDirectFile(t, ctx, clients, "/acme/payment/.gitslice/triggers/ci.js", `
import { label } from "./lib.js";
export default {
  async onEvent(event, gs) {
    console.log("event", event.event, label, event.changes.length);
    const check = await gs.checks.create({ name: "ci", status: "running", detailsUrl: "https://ci.example.com/1" });
    const echo = await fetch("https://httpbin.org/anything?t=" + gs.secret("CI_TOKEN")).then((r) => r.json());
    console.log("echoed", echo.args.t);
    const blocked = await fetch("https://example.org/").then((r) => r.status);
    console.log("blocked", blocked);
    await gs.state.set({ check: check.id });
  },
  async onCallback(request, gs) {
    const { check } = await gs.state.get();
    const body = await request.json();
    await gs.checks.update(check, { status: body.ok ? "passed" : "failed", summary: "from the callback" });
    return new Response("thanks", { status: 200 });
  },
};
`, "script")
	submitDirectFile(t, ctx, clients, "/acme/payment/.gitslice/triggers.yaml", `
version: 1
triggers:
  ci:
    on: [changeset.created, changeset.updated]
    paths: ["**/*.go"]
    checks: [ci]
    secrets: [CI_TOKEN]
    network: [httpbin.org]
    action:
      worker: triggers/ci.js
`, "triggers")

	changesetID, patchsetID := createDirectPatchset(t, ctx, clients, "/acme/payment/e2e/main.go", "package e2e\n", "e2e change")
	var run *corev1.TriggerRun
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := triggerClient.ListTriggerRuns(ctx, &corev1.ListTriggerRunsRequest{ChangesetId: changesetID})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs.Runs {
			if r.PatchsetId == patchsetID && r.TriggerId == "payment/ci" {
				run = r
			}
		}
		if run != nil && run.Status != "queued" && run.Status != "running" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if run == nil || run.Status != "waiting" {
		t.Fatalf("run = %v, want waiting", run)
	}
	logs, err := triggerClient.ListTriggerRunLogs(ctx, &corev1.ListTriggerRunLogsRequest{RunId: run.Id})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, l := range logs.Logs {
		text.WriteString(l.Chunk)
	}
	// The secret reached httpbin through the gate, and came back masked.
	for _, want := range []string{"event changeset.updated from-lib 1", "echoed ***", "blocked 403"} {
		if !strings.Contains(text.String(), want) && !strings.Contains(text.String(), strings.Replace(want, "changeset.updated", "changeset.created", 1)) {
			t.Fatalf("logs %q do not say %q", text.String(), want)
		}
	}
	if strings.Contains(text.String(), "e2e-s3cret-value") {
		t.Fatal("the secret value is in the logs")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("callback"))
	mac.Write([]byte{0})
	mac.Write([]byte(run.Id))
	callbackURL := dispatchURL + "/cb/" + run.Id + "/" + hex.EncodeToString(mac.Sum(nil))[:32]
	resp, err := http.Post(callbackURL, "application/json", strings.NewReader(`{"ok": true}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "thanks" {
		t.Fatalf("callback = %d %q", resp.StatusCode, body)
	}
	got, err := triggerClient.GetTriggerRun(ctx, &corev1.GetTriggerRunRequest{RunId: run.Id})
	if err != nil || got.Status != "succeeded" {
		t.Fatalf("run after the callback = %v, %v", got, err)
	}
	checks, err := checkClient.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: changesetID, PatchsetId: patchsetID})
	if err != nil || len(checks.Runs) != 1 || checks.Runs[0].Status != "passed" || checks.Runs[0].Summary != "from the callback" {
		t.Fatalf("checks = %v, %v", checks, err)
	}
	// A second callback after the run ended is gone.
	resp, err = http.Post(callbackURL, "application/json", strings.NewReader(`{"ok": true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("callback after the run ended = %d, want 410", resp.StatusCode)
	}
}
