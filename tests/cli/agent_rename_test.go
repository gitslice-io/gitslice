package cli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestAgentRenameNamesADaemonEverywhereItIsShown renames a daemon from the CLI
// and checks the name where a person meets the daemon: gs agent status, a
// slice's CI daemon, and the check runs of a changeset.
func TestAgentRenameNamesADaemonEverywhereItIsShown(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	ctx, cancel := context.WithTimeout(grpcAuthContext(readToken(t, home)), 60*time.Second)
	defer cancel()
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	stream, err := corev1.NewAgentServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{Name: "grok-bot-ci", Runtime: "test", Version: "0.0.1"}}}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	reg, err := stream.Recv()
	if err != nil || reg.GetRegistered().GetDaemonId() == "" {
		t.Fatalf("register: %v, %v", reg, err)
	}
	id := reg.GetRegistered().GetDaemonId()
	runCLI(t, home, workspace, "slice", "set-ci-daemon", "acme/payment", id)

	type daemonJSON struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		DisplayName string   `json:"display_name"`
		Mine        bool     `json:"mine"`
		CISlices    []string `json:"ci_slices"`
	}
	status := func() daemonJSON {
		t.Helper()
		var out struct {
			Daemons []daemonJSON `json:"daemons"`
		}
		if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "agent", "status", "--json")), &out); err != nil || len(out.Daemons) != 1 {
			t.Fatalf("gs agent status --json = %+v, %v", out, err)
		}
		return out.Daemons[0]
	}
	if d := status(); d.ID != id || d.Name != "grok-bot-ci" || d.DisplayName != "" || !d.Mine || len(d.CISlices) != 1 || d.CISlices[0] != "acme:payment" {
		t.Fatalf("before renaming: %+v", d)
	}

	// By the name it registered with, or by id.
	if out := runCLI(t, home, workspace, "agent", "rename", "grok-bot-ci", "Main CI"); !strings.Contains(out, "Main CI") || !strings.Contains(out, "grok-bot-ci") {
		t.Fatalf("gs agent rename: %s", out)
	}
	if d := status(); d.DisplayName != "Main CI" || d.Name != "grok-bot-ci" {
		t.Fatalf("after renaming: %+v", d)
	}
	if text := runCLI(t, home, workspace, "agent", "status"); !strings.Contains(text, "Main CI (grok-bot-ci)") || !strings.Contains(text, "acme:payment") {
		t.Fatalf("gs agent status:\n%s", text)
	}
	if show := runCLI(t, home, workspace, "slice", "info", "acme/payment"); !strings.Contains(show, "ci_daemon: Main CI") {
		t.Fatalf("gs slice info:\n%s", show)
	}
	runCLI(t, home, workspace, "agent", "rename", id, "Primary CI")
	if d := status(); d.DisplayName != "Primary CI" {
		t.Fatalf("renamed by id: %+v", d)
	}
	// Found by the name it is shown by.
	runCLI(t, home, workspace, "agent", "rename", "primary ci", "--clear")
	if d := status(); d.DisplayName != "" {
		t.Fatalf("after --clear: %+v", d)
	}
	if show := runCLI(t, home, workspace, "slice", "info", "acme/payment"); !strings.Contains(show, "ci_daemon: grok-bot-ci") {
		t.Fatalf("gs slice info after clearing:\n%s", show)
	}

	if _, stderr := runCLIFails(t, home, workspace, "agent", "rename", "no-such-daemon", "x"); !strings.Contains(stderr, "no agent daemon no-such-daemon") {
		t.Fatalf("renaming a daemon that is not there: %s", stderr)
	}
	if _, stderr := runCLIFails(t, home, workspace, "agent", "rename", id); !strings.Contains(stderr, "give a new name, or --clear") {
		t.Fatalf("gs agent rename with no name: %s", stderr)
	}
}
