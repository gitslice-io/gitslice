package cli

import (
	"os"
	"path/filepath"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func collectCheckRunUpdates(ch <-chan *corev1.DaemonMessage, runID string) []*corev1.CheckRunUpdate {
	var updates []*corev1.CheckRunUpdate
	for {
		select {
		case msg := <-ch:
			update := msg.GetCheckUpdate()
			if update != nil && update.GetRunId() == runID {
				updates = append(updates, update)
			}
		default:
			return updates
		}
	}
}

func collectCheckRunSeqs(ch <-chan *corev1.DaemonMessage, runID string) []int64 {
	updates := collectCheckRunUpdates(ch, runID)
	seqs := make([]int64, 0, len(updates))
	for _, update := range updates {
		seqs = append(seqs, update.GetClientSeq())
	}
	return seqs
}

func pendingCheckRunSeqs(t *testing.T, d *agentDaemon, runID string) []int64 {
	t.Helper()
	outbox := checkRunOutboxForTest(t, d, runID)
	outbox.OutMu.Lock()
	defer outbox.OutMu.Unlock()
	seqs := make([]int64, 0, len(outbox.Pending))
	for _, update := range outbox.Pending {
		seqs = append(seqs, update.GetClientSeq())
	}
	return seqs
}

func checkRunOutboxForTest(t *testing.T, d *agentDaemon, runID string) *checkRunOutbox {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	outbox := d.checkRunOutboxes[runID]
	if outbox == nil {
		t.Fatalf("missing check run outbox for %s", runID)
	}
	return outbox
}

func checkRunOutboxExists(d *agentDaemon, runID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.checkRunOutboxes[runID] != nil
}

func newTestAgentSendQueue(buffer int) *agentSendQueue {
	return &agentSendQueue{
		Ch:   make(chan *corev1.DaemonMessage, buffer),
		Done: make(chan struct{}),
	}
}

func sameInt64s(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func newTestCheckDaemon(t *testing.T) *agentDaemon {
	t.Helper()
	t.Setenv("GS_CLIENT_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GITSLICE_CLIENT_CACHE_DIR", "")
	return &agentDaemon{runner: Runner{Home: t.TempDir()}}
}

func assertFileContent(t *testing.T, target, want string) {
	t.Helper()
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s content = %q, want %q", target, data, want)
	}
}

func assertFileMode(t *testing.T, target string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", target, got, want)
	}
}
