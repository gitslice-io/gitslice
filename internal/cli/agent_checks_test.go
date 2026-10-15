package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestHandleRunChecksDedupsRecentlyCompletedRunID(t *testing.T) {
	repo := newFakeCheckRepo(map[string][]*corev1.TreeEntry{"/": nil}, nil)
	addr, stop := startFakeCheckRepoServer(t, repo)
	defer stop()

	sendQueue := &agentSendQueue{
		ch:   make(chan *corev1.DaemonMessage, 16),
		done: make(chan struct{}),
	}
	d := newTestCheckDaemon(t)
	d.cfg = UserConfig{ServerAddr: addr}
	d.baseCtx = context.Background()
	d.sendQueue = sendQueue
	d.checkRuns = map[string]context.CancelFunc{}
	req := &corev1.RunChecks{
		ResultTreeId: "tree-1",
		ServerAddr:   addr,
		Checks: []*corev1.CheckRunSpec{{
			RunId:            "run-dup",
			Name:             "unit",
			Command:          "printf ran",
			MaterializePaths: []string{"/"},
		}},
	}

	d.handleRunChecks(req)
	d.handleRunChecks(req)

	terminal := 0
	for {
		select {
		case msg := <-sendQueue.ch:
			update := msg.GetCheckUpdate()
			if update != nil && update.GetRunId() == "run-dup" && update.GetFinal() {
				terminal++
			}
		default:
			if terminal != 1 {
				t.Fatalf("terminal updates for duplicate run_id = %d, want 1", terminal)
			}
			return
		}
	}
}

func TestHandleRunChecksEmitsRunningBeforeTerminal(t *testing.T) {
	repo := newFakeCheckRepo(map[string][]*corev1.TreeEntry{"/": nil}, nil)
	addr, stop := startFakeCheckRepoServer(t, repo)
	defer stop()

	sendQueue := &agentSendQueue{
		ch:   make(chan *corev1.DaemonMessage, 16),
		done: make(chan struct{}),
	}
	d := newTestCheckDaemon(t)
	d.cfg = UserConfig{ServerAddr: addr}
	d.baseCtx = context.Background()
	d.sendQueue = sendQueue
	d.checkRuns = map[string]context.CancelFunc{}
	req := &corev1.RunChecks{
		ResultTreeId: "tree-1",
		ServerAddr:   addr,
		Checks: []*corev1.CheckRunSpec{{
			RunId:            "run-running",
			Name:             "unit",
			Command:          "printf done",
			MaterializePaths: []string{"/"},
		}},
	}

	d.handleRunChecks(req)

	updates := collectCheckRunUpdates(sendQueue.ch, "run-running")
	if len(updates) < 2 {
		t.Fatalf("check updates = %d, want running and terminal", len(updates))
	}
	running := updates[0]
	if running.GetStatus() != "running" {
		t.Fatalf("first update status = %q, want running", running.GetStatus())
	}
	if running.GetFinal() {
		t.Fatal("running update Final = true, want false")
	}
	if running.GetClientSeq() != 1 {
		t.Fatalf("running update client_seq = %d, want 1", running.GetClientSeq())
	}
	var terminal *corev1.CheckRunUpdate
	for _, update := range updates {
		if update.GetFinal() {
			terminal = update
		}
	}
	if terminal == nil {
		t.Fatalf("updates missing terminal update: %#v", updates)
	}
	if terminal.GetClientSeq() <= running.GetClientSeq() {
		t.Fatalf("terminal client_seq = %d, want after running seq %d", terminal.GetClientSeq(), running.GetClientSeq())
	}
}

func TestCheckRunUpdatesResentAfterQueueSwap(t *testing.T) {
	d := newTestCheckDaemon(t)
	firstQueue := newTestAgentSendQueue(8)
	d.sendQueue = firstQueue

	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: "run-resend", Status: "running", ClientSeq: 1})
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: "run-resend", LogChunk: "hello\n", Stream: "stdout", ClientSeq: 2})

	if got := collectCheckRunSeqs(firstQueue.ch, "run-resend"); !sameInt64s(got, []int64{1, 2}) {
		t.Fatalf("first queue seqs = %v, want [1 2]", got)
	}

	secondQueue := newTestAgentSendQueue(8)
	d.sendQueue = secondQueue
	d.resendPendingAll()

	if got := collectCheckRunSeqs(secondQueue.ch, "run-resend"); !sameInt64s(got, []int64{1, 2}) {
		t.Fatalf("second queue resent seqs = %v, want [1 2]", got)
	}
}

func TestCheckRunAckPrunesPendingBuffer(t *testing.T) {
	d := newTestCheckDaemon(t)
	for seq := int64(1); seq <= 3; seq++ {
		d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: "run-ack", LogChunk: fmt.Sprintf("line %d\n", seq), Stream: "stdout", ClientSeq: seq})
	}

	d.handleCheckRunAck(&corev1.CheckRunAck{RunId: "run-ack", AckedClientSeq: 2})

	pending := pendingCheckRunSeqs(t, d, "run-ack")
	if !sameInt64s(pending, []int64{3}) {
		t.Fatalf("pending seqs after ack = %v, want [3]", pending)
	}
}

func TestCheckRunTerminalAckRemovesOutbox(t *testing.T) {
	d := newTestCheckDaemon(t)
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: "run-terminal", Status: "running", ClientSeq: 1})
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: "run-terminal", Status: "passed", ClientSeq: 2, Final: true})

	d.handleCheckRunAck(&corev1.CheckRunAck{RunId: "run-terminal", AckedClientSeq: 1})
	if !checkRunOutboxExists(d, "run-terminal") {
		t.Fatal("outbox removed before terminal update was acked")
	}

	d.handleCheckRunAck(&corev1.CheckRunAck{RunId: "run-terminal", AckedClientSeq: 2})
	if checkRunOutboxExists(d, "run-terminal") {
		t.Fatal("outbox still exists after terminal update was acked")
	}
}

func TestCheckRunPendingCapDropsLogOnlyUpdatesFirst(t *testing.T) {
	d := newTestCheckDaemon(t)
	runID := "run-cap"
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: runID, Status: "running", ClientSeq: 1})
	for seq := int64(2); seq <= int64(agentMaxPendingCheckRunUpdates+5); seq++ {
		d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: runID, LogChunk: fmt.Sprintf("line %d\n", seq), Stream: "stdout", ClientSeq: seq})
	}
	terminalSeq := int64(agentMaxPendingCheckRunUpdates + 6)
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: runID, Status: "passed", ClientSeq: terminalSeq, Final: true})

	outbox := checkRunOutboxForTest(t, d, runID)
	outbox.outMu.Lock()
	defer outbox.outMu.Unlock()
	if got := len(outbox.pending); got != agentMaxPendingCheckRunUpdates {
		t.Fatalf("pending len = %d, want %d", got, agentMaxPendingCheckRunUpdates)
	}
	if first := outbox.pending[0]; first.GetClientSeq() != 1 || first.GetStatus() != "running" {
		t.Fatalf("first pending update = seq %d status %q, want running seq 1", first.GetClientSeq(), first.GetStatus())
	}
	last := outbox.pending[len(outbox.pending)-1]
	if last.GetClientSeq() != terminalSeq || !last.GetFinal() {
		t.Fatalf("last pending update = seq %d final %v, want terminal seq %d", last.GetClientSeq(), last.GetFinal(), terminalSeq)
	}
	for _, update := range outbox.pending {
		if update.GetClientSeq() == 2 {
			t.Fatal("oldest log-only update seq 2 survived cap trimming")
		}
	}
}

func TestHandleRunChecksHonorsCheckConcurrency(t *testing.T) {
	repo := newFakeCheckRepo(map[string][]*corev1.TreeEntry{"/": nil}, nil)
	addr, stop := startFakeCheckRepoServer(t, repo)
	defer stop()

	sendQueue := &agentSendQueue{
		ch:   make(chan *corev1.DaemonMessage, 64),
		done: make(chan struct{}),
	}
	d := newTestCheckDaemon(t)
	d.cfg = UserConfig{ServerAddr: addr}
	d.baseCtx = context.Background()
	d.sendQueue = sendQueue
	d.checkRuns = map[string]context.CancelFunc{}
	d.checkSlots = make(chan struct{}, 1)

	marks := t.TempDir()
	request := func(name string) *corev1.RunChecks {
		// Each check appends start and end marks; with one slot they must
		// never interleave.
		return &corev1.RunChecks{
			ResultTreeId: "tree-1",
			ServerAddr:   addr,
			Checks: []*corev1.CheckRunSpec{{
				RunId:            "run-" + name,
				Name:             name,
				Command:          fmt.Sprintf("echo start >> %[1]s/log; sleep 0.3; echo end >> %[1]s/log", marks),
				MaterializePaths: []string{"/"},
			}},
		}
	}
	var wg sync.WaitGroup
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(req *corev1.RunChecks) {
			defer wg.Done()
			d.handleRunChecks(req)
		}(request(name))
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(marks, "log"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(data)); strings.Join(got, " ") != "start end start end" {
		t.Fatalf("checks overlapped: %v", got)
	}
}
