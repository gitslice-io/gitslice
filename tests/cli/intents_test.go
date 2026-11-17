package cli_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/proto/core/v1/corev1connect"
)

func TestAdvisoryPathIntentsAndOverlapWarningsEndToEnd(t *testing.T) {
	ts := startTestServer(t)

	aliceHome := t.TempDir()
	aliceWS := t.TempDir()
	loginTestCLI(t, ts, aliceHome, aliceWS)
	aliceToken := readToken(t, aliceHome)

	bobToken, bobSubject, _ := ts.provisionAccount(t, "bob-dev", "bob-dev")
	bobHome := t.TempDir()
	bobWS := t.TempDir()
	writeCLIAuthConfig(t, bobHome, ts.addr, bobToken, bobSubject)

	// Grant bob contributor access on acme:payment so bob can propose changes and intents.
	runCLI(t, aliceHome, aliceWS, "slice", "share", "acme:payment", "bob-dev", "--role", "contributor")

	runCLI(t, aliceHome, aliceWS, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, aliceWS, "acme/payment/billing/tax.go", "package billing\nconst Rate = 10\n")
	writeWorkspaceFile(t, aliceWS, "acme/payment/other.go", "package payment\nconst Base = 1\n")
	runCLI(t, aliceHome, aliceWS, "create", "--all", "--message", "seed billing", "--submit")

	runCLI(t, bobHome, bobWS, "workspace", "init", "acme/payment")

	// 1. Alice declares an advisory intent on billing/** inside her slice workspace.
	intentJSON := runCLI(t, aliceHome, aliceWS, "intent", "billing/**", "--for", "2h", "-m", "rewriting tax engine", "--json")
	var declared struct {
		ID      string `json:"id"`
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal([]byte(intentJSON), &declared); err != nil {
		t.Fatalf("unmarshal intent JSON: %v\n%s", err, intentJSON)
	}
	if declared.ID == "" || declared.Pattern != "/acme/payment/billing/**" {
		t.Fatalf("unexpected declared intent: %+v", declared)
	}

	// Verify Connect HTTP adapter also serves ListPathIntents cleanly.
	connectClient := corev1connect.NewChangesetServiceClient(http.DefaultClient, "http://"+ts.httpAddr)
	connectReq := connect.NewRequest(&corev1.ListPathIntentsRequest{
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	connectReq.Header().Set("Authorization", "Bearer "+bobToken)
	connectResp, err := connectClient.ListPathIntents(t.Context(), connectReq)
	if err != nil || len(connectResp.Msg.GetIntents()) != 1 || connectResp.Msg.GetIntents()[0].GetId() != declared.ID {
		t.Fatalf("Connect ListPathIntents = %+v, err=%v", connectResp, err)
	}

	// 2. Bob edits billing/tax.go -> gs status, gs sync, and gs create warn about Alice's intent without blocking!
	writeWorkspaceFile(t, bobWS, "acme/payment/billing/tax.go", "package billing\nconst Rate = 15\n")

	statusText := runCLI(t, bobHome, bobWS, "status")
	if !strings.Contains(statusText, "warning:") || !strings.Contains(statusText, declared.ID) || !strings.Contains(statusText, "rewriting tax engine") {
		t.Fatalf("gs status missing overlap warning:\n%s", statusText)
	}

	// --local must skip remote overlap checks completely.
	localStatusJSON := runCLI(t, bobHome, bobWS, "status", "--local", "--json")
	var localParsed struct {
		LocalOnly       string `json:"local_only"`
		OverlapWarnings []any  `json:"overlap_warnings"`
	}
	if err := json.Unmarshal([]byte(localStatusJSON), &localParsed); err != nil {
		t.Fatalf("status --local --json: %v\n%s", err, localStatusJSON)
	}
	if localParsed.LocalOnly != "local" || len(localParsed.OverlapWarnings) != 0 {
		t.Fatalf("expected zero overlap_warnings with --local, got: %s", localStatusJSON)
	}

	// Land an unrelated change from Alice so Bob's gs sync actually syncs while preserving Bob's dirty billing/tax.go,
	// and verify gs sync emits the overlap warning!
	writeWorkspaceFile(t, aliceWS, "acme/payment/other.go", "package payment\nconst Base = 2\n")
	runCLI(t, aliceHome, aliceWS, "create", "--all", "--message", "bump base", "--submit")
	// Alice's unrelated change did NOT clear Alice's billing/** intent.
	listStillActive := runCLI(t, bobHome, bobWS, "intent", "list")
	if !strings.Contains(listStillActive, declared.ID) {
		t.Fatalf("unrelated submit cleared billing intent:\n%s", listStillActive)
	}

	syncText := runCLI(t, bobHome, bobWS, "sync")
	if !strings.Contains(syncText, "synced acme:payment") || !strings.Contains(syncText, "warning:") || !strings.Contains(syncText, declared.ID) {
		t.Fatalf("gs sync missing overlap warning:\n%s", syncText)
	}

	// Bob runs gs create: primary line starts with "created ", followed by advisory warning, and JSON carries overlap_warnings!
	createText := runCLI(t, bobHome, bobWS, "create", "--all", "--message", "bob tax tweak")
	if !strings.HasPrefix(createText, "created ") || !strings.Contains(createText, "warning:") || !strings.Contains(createText, declared.ID) {
		t.Fatalf("gs create output missing 'created ' prefix or warning:\n%s", createText)
	}

	// 3. Now Bob has an open draft changeset touching billing/tax.go!
	// Alice edits billing/tax.go -> Alice does NOT warn on her OWN intent,
	// AND DOES warn on Bob's open draft changeset!
	runCLI(t, aliceHome, aliceWS, "sync")
	writeWorkspaceFile(t, aliceWS, "acme/payment/billing/tax.go", "package billing\nconst Rate = 20\n")
	aliceStatusJSON := runCLI(t, aliceHome, aliceWS, "status", "--json")
	var aliceStatus struct {
		OverlapWarnings []struct {
			Kind    string `json:"kind"`
			Owner   string `json:"owner"`
			Message string `json:"message"`
		} `json:"overlap_warnings"`
	}
	if err := json.Unmarshal([]byte(aliceStatusJSON), &aliceStatus); err != nil {
		t.Fatalf("alice status --json: %v\n%s", err, aliceStatusJSON)
	}
	if len(aliceStatus.OverlapWarnings) != 1 || aliceStatus.OverlapWarnings[0].Kind != "changeset" || aliceStatus.OverlapWarnings[0].Owner != "bob-dev" {
		t.Fatalf("expected exactly 1 changeset warning (not own intent) for alice, got: %s", aliceStatusJSON)
	}

	// 4. Alice lands a changeset under billing/** -> her billing/** intent auto-clears on land!
	_ = aliceToken
	runCLI(t, aliceHome, aliceWS, "create", "--all", "--message", "finish tax engine", "--submit")
	afterLandList := runCLI(t, bobHome, bobWS, "intent", "list")
	if strings.Contains(afterLandList, declared.ID) || !strings.Contains(afterLandList, "no active path intents") {
		t.Fatalf("expected Alice's intent to auto-clear on land, got:\n%s", afterLandList)
	}
}
