package cli_test

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

// TestFeedbackLoop checks the feedback loop through gs: an agent sends
// feedback from its workspace, the team (an operator here) reads it with the
// context gs added and answers it, and the agent reads the answer.
func TestFeedbackLoop(t *testing.T) {
	ts := startTestServer(t)
	operatorToken, _, operatorSubject := ts.provisionAccount(t, "fb-operator", "fb-operator")
	ts.setOperators(t, operatorSubject)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	type feedback struct {
		ID         string            `json:"id"`
		Account    string            `json:"account"`
		Body       string            `json:"body"`
		Category   string            `json:"category"`
		Context    map[string]string `json:"context"`
		Status     string            `json:"status"`
		Resolution string            `json:"resolution"`
		ResolvedBy string            `json:"resolved_by"`
	}
	var sent feedback
	out := runCLI(t, home, workspace, "feedback", "the 1,742-file upload ran silent", "--category", "bug", "--context", "command=gs fs upload", "--json")
	if err := json.Unmarshal([]byte(out), &sent); err != nil {
		t.Fatalf("feedback --json: %v\n%s", err, out)
	}
	if !strings.HasPrefix(sent.ID, "fb_") || sent.Body != "the 1,742-file upload ran silent" || sent.Category != "bug" || sent.Status != "open" ||
		sent.Context["workspace"] != "acme/payment" || sent.Context["os"] != runtime.GOOS || sent.Context["command"] != "gs fs upload" || sent.Context["gs_version"] == "" {
		t.Fatalf("sent = %+v", sent)
	}
	text, _ := runCLIStreamsWithInput(t, home, workspace, "sync of 440 paths was silent too\n", "feedback", "send", "--stdin", "--category", "idea")
	if !strings.HasPrefix(text, "sent feedback fb_") || !strings.Contains(text, "gs feedback list --mine") {
		t.Fatalf("send output:\n%s", text)
	}
	if _, stderr := runCLIFails(t, home, workspace, "feedback"); !strings.Contains(stderr, "feedback message required") {
		t.Fatalf("empty feedback: %s", stderr)
	}

	// Everyone's feedback is for the team; the sender reads their own.
	if _, stderr := runCLIFails(t, home, workspace, "feedback", "list"); !strings.Contains(stderr, "only the server's operators") || !strings.Contains(stderr, "--mine") {
		t.Fatalf("list as a sender: %s", stderr)
	}
	if mine := runCLI(t, home, workspace, "feedback", "list", "--mine"); !strings.Contains(mine, sent.ID+"  open  bug") || !strings.Contains(mine, "  the 1,742-file upload ran silent") {
		t.Fatalf("list --mine:\n%s", mine)
	}

	// The team reads it with its context, and answers.
	opHome := t.TempDir()
	writeCLIAuthConfig(t, opHome, ts.addr, operatorToken, operatorSubject)
	var listed struct {
		Feedback []feedback `json:"feedback"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, opHome, t.TempDir(), "feedback", "list", "--json")), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Feedback) != 2 || listed.Feedback[1].ID != sent.ID || listed.Feedback[1].Context["workspace"] != "acme/payment" || listed.Feedback[0].Body != "sync of 440 paths was silent too" {
		t.Fatalf("team list = %+v", listed.Feedback)
	}
	if _, stderr := runCLIFails(t, opHome, t.TempDir(), "feedback", "resolve", sent.ID); !strings.Contains(stderr, "needs --note") {
		t.Fatalf("resolve without a note: %s", stderr)
	}
	if out := runCLI(t, opHome, t.TempDir(), "feedback", "resolve", sent.ID, "--note", "progress lines ship in v0.4.4"); !strings.HasPrefix(out, "resolved "+sent.ID) {
		t.Fatalf("resolve output: %s", out)
	}
	if out := runCLI(t, opHome, t.TempDir(), "feedback", "list"); strings.Contains(out, sent.ID) || !strings.Contains(out, "sync of 440 paths") {
		t.Fatalf("open after resolving:\n%s", out)
	}

	// The sender reads the answer.
	mine := runCLI(t, home, workspace, "feedback", "list", "--mine", "--status", "resolved")
	if !strings.Contains(mine, "  answer from fb-operator, ") || !strings.Contains(mine, "    progress lines ship in v0.4.4") || strings.Contains(mine, "sync of 440 paths") {
		t.Fatalf("sender's answers:\n%s", mine)
	}
}
