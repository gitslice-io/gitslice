package agentd

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

	sendQueue := &SendQueue{
		Ch:   make(chan *corev1.DaemonMessage, 16),
		Done: make(chan struct{}),
	}
	d := newTestCheckDaemon(t)
	d.Host.ServerAddr = addr
	d.BaseCtx = context.Background()
	d.SendQueue = sendQueue
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

	d.HandleRunChecks(req)
	d.HandleRunChecks(req)

	terminal := 0
	for {
		select {
		case msg := <-sendQueue.Ch:
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

	sendQueue := &SendQueue{
		Ch:   make(chan *corev1.DaemonMessage, 16),
		Done: make(chan struct{}),
	}
	d := newTestCheckDaemon(t)
	d.Host.ServerAddr = addr
	d.BaseCtx = context.Background()
	d.SendQueue = sendQueue
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

	d.HandleRunChecks(req)

	updates := collectCheckRunUpdates(sendQueue.Ch, "run-running")
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
	d.SendQueue = firstQueue

	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: "run-resend", Status: "running", ClientSeq: 1})
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: "run-resend", LogChunk: "hello\n", Stream: "stdout", ClientSeq: 2})

	if got := collectCheckRunSeqs(firstQueue.Ch, "run-resend"); !sameInt64s(got, []int64{1, 2}) {
		t.Fatalf("first queue seqs = %v, want [1 2]", got)
	}

	secondQueue := newTestAgentSendQueue(8)
	d.SendQueue = secondQueue
	d.resendPendingAll()

	if got := collectCheckRunSeqs(secondQueue.Ch, "run-resend"); !sameInt64s(got, []int64{1, 2}) {
		t.Fatalf("second queue resent seqs = %v, want [1 2]", got)
	}
}

func TestCheckRunAckPrunesPendingBuffer(t *testing.T) {
	d := newTestCheckDaemon(t)
	for seq := int64(1); seq <= 3; seq++ {
		d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: "run-ack", LogChunk: fmt.Sprintf("line %d\n", seq), Stream: "stdout", ClientSeq: seq})
	}

	d.HandleCheckRunAck(&corev1.CheckRunAck{RunId: "run-ack", AckedClientSeq: 2})

	pending := pendingCheckRunSeqs(t, d, "run-ack")
	if !sameInt64s(pending, []int64{3}) {
		t.Fatalf("pending seqs after ack = %v, want [3]", pending)
	}
}

func TestCheckRunTerminalAckRemovesOutbox(t *testing.T) {
	d := newTestCheckDaemon(t)
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: "run-terminal", Status: "running", ClientSeq: 1})
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: "run-terminal", Status: "passed", ClientSeq: 2, Final: true})

	d.HandleCheckRunAck(&corev1.CheckRunAck{RunId: "run-terminal", AckedClientSeq: 1})
	if !checkRunOutboxExists(d, "run-terminal") {
		t.Fatal("outbox removed before terminal update was acked")
	}

	d.HandleCheckRunAck(&corev1.CheckRunAck{RunId: "run-terminal", AckedClientSeq: 2})
	if checkRunOutboxExists(d, "run-terminal") {
		t.Fatal("outbox still exists after terminal update was acked")
	}
}

func TestCheckRunPendingCapDropsLogOnlyUpdatesFirst(t *testing.T) {
	d := newTestCheckDaemon(t)
	runID := "run-cap"
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: runID, Status: "running", ClientSeq: 1})
	for seq := int64(2); seq <= int64(MaxPendingCheckRunUpdates+5); seq++ {
		d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: runID, LogChunk: fmt.Sprintf("line %d\n", seq), Stream: "stdout", ClientSeq: seq})
	}
	terminalSeq := int64(MaxPendingCheckRunUpdates + 6)
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{RunId: runID, Status: "passed", ClientSeq: terminalSeq, Final: true})

	outbox := checkRunOutboxForTest(t, d, runID)
	outbox.OutMu.Lock()
	defer outbox.OutMu.Unlock()
	if got := len(outbox.Pending); got != MaxPendingCheckRunUpdates {
		t.Fatalf("pending len = %d, want %d", got, MaxPendingCheckRunUpdates)
	}
	if first := outbox.Pending[0]; first.GetClientSeq() != 1 || first.GetStatus() != "running" {
		t.Fatalf("first pending update = seq %d status %q, want running seq 1", first.GetClientSeq(), first.GetStatus())
	}
	last := outbox.Pending[len(outbox.Pending)-1]
	if last.GetClientSeq() != terminalSeq || !last.GetFinal() {
		t.Fatalf("last pending update = seq %d final %v, want terminal seq %d", last.GetClientSeq(), last.GetFinal(), terminalSeq)
	}
	for _, update := range outbox.Pending {
		if update.GetClientSeq() == 2 {
			t.Fatal("oldest log-only update seq 2 survived cap trimming")
		}
	}
}

func TestHandleRunChecksHonorsCheckConcurrency(t *testing.T) {
	repo := newFakeCheckRepo(map[string][]*corev1.TreeEntry{"/": nil}, nil)
	addr, stop := startFakeCheckRepoServer(t, repo)
	defer stop()

	sendQueue := &SendQueue{
		Ch:   make(chan *corev1.DaemonMessage, 64),
		Done: make(chan struct{}),
	}
	d := newTestCheckDaemon(t)
	d.Host.ServerAddr = addr
	d.BaseCtx = context.Background()
	d.SendQueue = sendQueue
	d.checkRuns = map[string]context.CancelFunc{}
	d.CheckSlots = make(chan struct{}, 1)

	marks := t.TempDir()
	request := func(name string) *corev1.RunChecks {
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
			d.HandleRunChecks(req)
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

func TestHandleRunChecksRunsOneRequestsChecksSideBySide(t *testing.T) {
	repo := newFakeCheckRepo(map[string][]*corev1.TreeEntry{"/": nil}, nil)
	addr, stop := startFakeCheckRepoServer(t, repo)
	defer stop()

	sendQueue := &SendQueue{
		Ch:   make(chan *corev1.DaemonMessage, 64),
		Done: make(chan struct{}),
	}
	d := newTestCheckDaemon(t)
	d.Host.ServerAddr = addr
	d.BaseCtx = context.Background()
	d.SendQueue = sendQueue
	d.checkRuns = map[string]context.CancelFunc{}
	d.CheckSlots = make(chan struct{}, 2)

	marks := t.TempDir()
	spec := func(name, other string) *corev1.CheckRunSpec {
		return &corev1.CheckRunSpec{
			RunId:            "run-" + name,
			Name:             name,
			Command:          fmt.Sprintf("touch %[1]s/%[2]s; for i in $(seq 50); do [ -e %[1]s/%[3]s ] && echo met > %[1]s/%[2]s.ok && exit 0; sleep 0.1; done; exit 1", marks, name, other),
			MaterializePaths: []string{"/"},
		}
	}
	d.HandleRunChecks(&corev1.RunChecks{
		ResultTreeId: "tree-1",
		ServerAddr:   addr,
		Checks:       []*corev1.CheckRunSpec{spec("one", "two"), spec("two", "one")},
	})
	for _, name := range []string{"one", "two"} {
		if _, err := os.Stat(filepath.Join(marks, name+".ok")); err != nil {
			t.Fatalf("check %s never saw the other one running: the checks ran one after the other", name)
		}
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

	sendQueue := &SendQueue{
		Ch:   make(chan *corev1.DaemonMessage, 16),
		Done: make(chan struct{}),
	}
	d := newTestCheckDaemon(t)
	d.Host.ServerAddr = addr
	d.BaseCtx = context.Background()
	d.SendQueue = sendQueue

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
	d.HandleRunChecks(req)

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
