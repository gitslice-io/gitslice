package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitslice-io/gitslice/internal/checkexec"
	"github.com/gitslice-io/gitslice/internal/checks"
	"github.com/gitslice-io/gitslice/internal/clientcache"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

const (
	checkMaterializePageSize        = 1000
	checkMaterializeDirConcurrency  = 32
	checkMaterializeReadConcurrency = 32
	agentMaxPendingCheckRunUpdates  = 256
	agentMaxCheckRunOutboxes        = 128
)

type RepoClient interface {
	ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest, opts ...grpc.CallOption) (*corev1.ListDirectoryResponse, error)
	ReadFile(ctx context.Context, req *corev1.ReadFileRequest, opts ...grpc.CallOption) (*corev1.ReadFileResponse, error)
}

type checkRunOutbox struct {
	outMu          sync.Mutex
	pending        []*corev1.CheckRunUpdate
	ackedClientSeq int64
	flushedSeq     int64
	flushQueue     *agentSendQueue
	terminal       bool
}

// materializeCheckTree fetches the given repo-root-relative path prefixes of
// rootTreeID from the server into destRoot, preserving repo-relative layout.
func (d *agentDaemon) materializeCheckTree(ctx context.Context, repo RepoClient, rootTreeID string, paths []string, destRoot string) error {
	if repo == nil {
		return fmt.Errorf("repository client is required")
	}
	if strings.TrimSpace(rootTreeID) == "" {
		return fmt.Errorf("root tree id is required")
	}
	if strings.TrimSpace(destRoot) == "" {
		return fmt.Errorf("destination root is required")
	}
	prefixes, err := normalizeCheckMaterializePrefixes(paths)
	if err != nil {
		return err
	}
	if len(prefixes) == 0 {
		prefixes = []string{"/"}
	}
	plan := newCheckMaterializePlan()
	for _, prefix := range prefixes {
		if prefix == "/" {
			if err := d.materializeCheckDirectory(ctx, repo, rootTreeID, "/", plan); err != nil {
				return err
			}
			continue
		}
		entry, err := lookupCheckTreeEntry(ctx, repo, rootTreeID, prefix)
		if err != nil {
			return err
		}
		switch entry.GetKind() {
		case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
			entryPath, err := entryLogicalPath(entry)
			if err != nil {
				return err
			}
			if err := d.materializeCheckDirectory(ctx, repo, rootTreeID, entryPath, plan); err != nil {
				return err
			}
		case corev1.EntryKind_ENTRY_KIND_FILE:
			if err := plan.addFile(entry); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported check tree entry kind for %s: %s", prefix, entryKindName(entry.GetKind()))
		}
	}
	for _, dir := range plan.sortedDirs() {
		if err := ctx.Err(); err != nil {
			return err
		}
		target, err := checkTreeLocalPath(destRoot, dir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
	}
	cache, err := d.runner.objectCache()
	if err != nil {
		return err
	}
	g, groupCtx := errgroup.WithContext(ctx)
	g.SetLimit(checkMaterializeReadConcurrency)
	for _, entry := range plan.sortedFiles() {
		entry := entry
		g.Go(func() error {
			return materializeCheckFile(groupCtx, repo, cache, rootTreeID, entry, destRoot)
		})
	}
	return g.Wait()
}

func (d *agentDaemon) materializeCheckDirectory(ctx context.Context, repo RepoClient, rootTreeID, dir string, plan *checkMaterializePlan) error {
	if plan == nil {
		return fmt.Errorf("check materialize plan is required")
	}
	dir, err := normalizeCheckMaterializePath(dir)
	if err != nil {
		return err
	}
	g, groupCtx := errgroup.WithContext(ctx)
	g.SetLimit(checkMaterializeDirConcurrency)
	g.Go(func() error {
		return d.materializeCheckDirectoryWalk(groupCtx, g, repo, rootTreeID, dir, plan)
	})
	return g.Wait()
}

func (d *agentDaemon) materializeCheckDirectoryWalk(ctx context.Context, g *errgroup.Group, repo RepoClient, rootTreeID, dir string, plan *checkMaterializePlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := plan.addDir(dir); err != nil {
		return err
	}
	entries, err := listCheckDirectoryAll(ctx, repo, &corev1.ListDirectoryRequest{
		RootTreeId: rootTreeID,
		Path:       logicalAbsPath(dir),
		PageSize:   checkMaterializePageSize,
	})
	if err != nil {
		return err
	}
	var subdirs []string
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		switch entry.GetKind() {
		case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
			entryPath, err := entryLogicalPath(entry)
			if err != nil {
				return err
			}
			subdirs = append(subdirs, entryPath)
		case corev1.EntryKind_ENTRY_KIND_FILE:
			if err := plan.addFile(entry); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported check tree entry kind for %s: %s", entry.GetPath(), entryKindName(entry.GetKind()))
		}
	}
	for _, subdir := range subdirs {
		subdir := subdir
		if g.TryGo(func() error {
			return d.materializeCheckDirectoryWalk(ctx, g, repo, rootTreeID, subdir, plan)
		}) {
			continue
		}
		if err := d.materializeCheckDirectoryWalk(ctx, g, repo, rootTreeID, subdir, plan); err != nil {
			return err
		}
	}
	return nil
}

type checkMaterializePlan struct {
	mu    sync.Mutex
	dirs  map[string]struct{}
	files map[string]*corev1.TreeEntry
}

func newCheckMaterializePlan() *checkMaterializePlan {
	return &checkMaterializePlan{
		dirs:  map[string]struct{}{},
		files: map[string]*corev1.TreeEntry{},
	}
}

func (p *checkMaterializePlan) addDir(dir string) error {
	dir, err := normalizeCheckMaterializePath(dir)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dirs[dir] = struct{}{}
	return nil
}

func (p *checkMaterializePlan) addFile(entry *corev1.TreeEntry) error {
	entryPath, err := entryLogicalPath(entry)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files[entryPath] = entry
	p.dirs[checkMaterializeParentDir(entryPath)] = struct{}{}
	return nil
}

func (p *checkMaterializePlan) sortedDirs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.dirs))
	for dir := range p.dirs {
		out = append(out, dir)
	}
	sort.Slice(out, func(i, j int) bool {
		leftDepth := checkPathDepth(out[i])
		rightDepth := checkPathDepth(out[j])
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return out[i] < out[j]
	})
	return out
}

func (p *checkMaterializePlan) sortedFiles() []*corev1.TreeEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	paths := make([]string, 0, len(p.files))
	for filePath := range p.files {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	out := make([]*corev1.TreeEntry, 0, len(paths))
	for _, filePath := range paths {
		out = append(out, p.files[filePath])
	}
	return out
}

func checkMaterializeParentDir(filePath string) string {
	if filePath == "/" || !strings.Contains(filePath, "/") {
		return "/"
	}
	return filePath[:strings.LastIndex(filePath, "/")]
}

func materializeCheckFile(ctx context.Context, repo RepoClient, cache *clientcache.ObjectCache, rootTreeID string, entry *corev1.TreeEntry, destRoot string) error {
	entryPath, err := entryLogicalPath(entry)
	if err != nil {
		return err
	}
	target, err := checkTreeLocalPath(destRoot, entryPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	data, err := materializeCheckFileBytes(ctx, repo, cache, rootTreeID, entryPath, entry.GetContentHash())
	if err != nil {
		return err
	}
	mode := entry.GetMode()
	if mode == 0 {
		mode = 0o100644
	}
	_, err = writeWorkspaceFile(target, mode, bytes.NewReader(data))
	return err
}

func materializeCheckFileBytes(ctx context.Context, repo RepoClient, cache *clientcache.ObjectCache, rootTreeID, entryPath, contentHash string) ([]byte, error) {
	if contentHash != "" && cache != nil {
		if cache.Exists(contentHash) {
			return cache.Read(contentHash)
		}
		read, err := repo.ReadFile(ctx, &corev1.ReadFileRequest{
			RootTreeId: rootTreeID,
			Path:       logicalAbsPath(entryPath),
		})
		if err != nil {
			return nil, err
		}
		cached, err := cache.PutBytes(read.GetData())
		if err != nil {
			return nil, err
		}
		if cached.ContentHash != contentHash {
			return nil, fmt.Errorf("check materialize content hash mismatch for %s: got %s, want %s", entryPath, cached.ContentHash, contentHash)
		}
		return read.GetData(), nil
	}
	read, err := repo.ReadFile(ctx, &corev1.ReadFileRequest{
		RootTreeId: rootTreeID,
		Path:       logicalAbsPath(entryPath),
	})
	if err != nil {
		return nil, err
	}
	return read.GetData(), nil
}

func lookupCheckTreeEntry(ctx context.Context, repo RepoClient, rootTreeID, prefix string) (*corev1.TreeEntry, error) {
	prefix, err := normalizeCheckMaterializePath(prefix)
	if err != nil {
		return nil, err
	}
	parent := "/"
	base := prefix
	if slash := strings.LastIndex(prefix, "/"); slash >= 0 {
		parent = prefix[:slash]
		if parent == "" {
			parent = "/"
		}
		base = prefix[slash+1:]
	}
	entries, err := listCheckDirectoryAll(ctx, repo, &corev1.ListDirectoryRequest{
		RootTreeId: rootTreeID,
		Path:       logicalAbsPath(parent),
		PageSize:   checkMaterializePageSize,
	})
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		entryPath, err := entryLogicalPath(entry)
		if err == nil && entryPath == prefix {
			return entry, nil
		}
		if entry.GetName() == base {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("check materialize path not found: %s", prefix)
}

func listCheckDirectoryAll(ctx context.Context, repo RepoClient, req *corev1.ListDirectoryRequest) ([]*corev1.TreeEntry, error) {
	if req == nil {
		req = &corev1.ListDirectoryRequest{}
	}
	pageReq := &corev1.ListDirectoryRequest{
		CommitId:   req.GetCommitId(),
		Path:       req.GetPath(),
		Cursor:     req.GetCursor(),
		PageSize:   req.GetPageSize(),
		Slice:      req.GetSlice(),
		RootTreeId: req.GetRootTreeId(),
	}
	var entries []*corev1.TreeEntry
	for {
		page, err := repo.ListDirectory(ctx, pageReq)
		if err != nil {
			return nil, err
		}
		entries = append(entries, page.GetEntries()...)
		if strings.TrimSpace(page.GetNextCursor()) == "" {
			return entries, nil
		}
		if page.GetNextCursor() == pageReq.Cursor {
			return nil, fmt.Errorf("directory pagination did not advance for %s", pageReq.Path)
		}
		pageReq.Cursor = page.GetNextCursor()
	}
}

func normalizeCheckMaterializePrefixes(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		p, err := normalizeCheckMaterializePath(value)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		cleaned = append(cleaned, p)
	}
	sort.Slice(cleaned, func(i, j int) bool {
		leftDepth := checkPathDepth(cleaned[i])
		rightDepth := checkPathDepth(cleaned[j])
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return cleaned[i] < cleaned[j]
	})
	out := make([]string, 0, len(cleaned))
	for _, candidate := range cleaned {
		nested := false
		for _, existing := range out {
			if checkPathContains(existing, candidate) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, candidate)
		}
	}
	return out, nil
}

func normalizeCheckMaterializePath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || value == "." || value == "/" {
		return "/", nil
	}
	trimmed := strings.TrimPrefix(value, "/")
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", fmt.Errorf("check materialize path %q contains .. segment", value)
		}
	}
	cleaned := path.Clean("/" + trimmed)
	if cleaned == "/" {
		return "/", nil
	}
	return strings.TrimPrefix(cleaned, "/"), nil
}

func entryLogicalPath(entry *corev1.TreeEntry) (string, error) {
	if entry == nil {
		return "", fmt.Errorf("tree entry is nil")
	}
	p := strings.TrimSpace(entry.GetPath())
	if p == "" {
		return "", fmt.Errorf("tree entry path is empty")
	}
	return normalizeCheckMaterializePath(p)
}

func checkPathDepth(p string) int {
	if p == "/" {
		return 0
	}
	return strings.Count(p, "/") + 1
}

func checkPathContains(prefix, p string) bool {
	if prefix == "/" {
		return true
	}
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func logicalAbsPath(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return "/" + path.Clean(p)
}

func checkTreeLocalPath(destRoot, logicalPath string) (string, error) {
	cleaned, err := normalizeCheckMaterializePath(logicalPath)
	if err != nil {
		return "", err
	}
	rel := "."
	if cleaned != "/" {
		rel = filepath.FromSlash(cleaned)
	}
	root, err := filepath.Abs(destRoot)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, rel)
	if err := ensureCheckLocalPathInside(root, target); err != nil {
		return "", err
	}
	return target, nil
}

func ensureCheckLocalPathInside(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("resolve check materialize path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("check materialize target escapes destination: %s", target)
	}
	return nil
}

func (d *agentDaemon) handleRunChecks(req *corev1.RunChecks) {
	if req == nil {
		return
	}
	baseCtx := d.checkBaseContext()
	serverAddr := strings.TrimSpace(req.GetServerAddr())
	if serverAddr == "" {
		serverAddr = d.cfg.ServerAddr
	}
	conn, err := dial(baseCtx, serverAddr)
	if err != nil {
		d.emitSetupFailureForRunChecks(req, fmt.Errorf("dial repository service: %w", err))
		return
	}
	defer conn.Close()

	repo := RepoClient(corev1.NewRepositoryServiceClient(conn))
	if account := strings.TrimSpace(req.GetSlice().GetAccount()); account != "" {
		repo = checkTreeRepoClient{inner: repo, account: account}
	}
	for _, spec := range req.GetChecks() {
		d.handleCheckRunSpec(req, repo, spec)
	}
}

func (d *agentDaemon) handleCheckRunSpec(req *corev1.RunChecks, repo RepoClient, spec *corev1.CheckRunSpec) {
	if spec == nil || strings.TrimSpace(spec.GetRunId()) == "" {
		return
	}
	baseCtx := d.checkBaseContext()
	runCtx, cancel := context.WithCancel(baseCtx)
	if !d.startCheckRun(spec.GetRunId(), cancel) {
		cancel()
		return
	}
	defer d.clearCheckRun(spec.GetRunId(), cancel)
	defer cancel()

	seq := int64(0)
	d.emitCheckRunRunning(spec.GetRunId(), &seq)

	tempDir, err := os.MkdirTemp("", "gitslice-check-*")
	if err != nil {
		d.emitCheckRunLog(spec.GetRunId(), &seq, err.Error())
		d.emitCheckRunTerminal(spec.GetRunId(), &seq, "errored", -1, oneLineSummary(err.Error()))
		return
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			fmt.Fprintf(d.runner.stderr(), "check workspace remove failed for %s: %v\n", tempDir, err)
		}
	}()

	if err := d.materializeCheckTree(authContext(runCtx, d.cfg), repo, req.GetResultTreeId(), spec.GetMaterializePaths(), tempDir); err != nil {
		if errors.Is(err, context.Canceled) && baseCtx.Err() == nil {
			d.emitCheckRunTerminal(spec.GetRunId(), &seq, "canceled", -1, "canceled")
			return
		}
		d.emitCheckRunLog(spec.GetRunId(), &seq, err.Error())
		d.emitCheckRunTerminal(spec.GetRunId(), &seq, "errored", -1, oneLineSummary(err.Error()))
		return
	}

	result, err := checkexec.Run(runCtx, tempDir, checkSpecFromProto(spec))
	if err != nil {
		result = checkexec.Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  oneLineSummary(err.Error()),
			Log:      err.Error(),
		}
	}
	if runCtx.Err() != nil && baseCtx.Err() == nil {
		result.Status = "canceled"
		result.ExitCode = -1
		result.Summary = "canceled"
	}
	if result.Log != "" {
		d.emitCheckRunLog(spec.GetRunId(), &seq, result.Log)
	}
	d.emitCheckRunTerminal(spec.GetRunId(), &seq, result.Status, result.ExitCode, oneLineSummary(result.Summary))
}

func (d *agentDaemon) emitSetupFailureForRunChecks(req *corev1.RunChecks, err error) {
	for _, spec := range req.GetChecks() {
		if spec == nil || strings.TrimSpace(spec.GetRunId()) == "" {
			continue
		}
		if !d.startCheckRun(spec.GetRunId(), func() {}) {
			continue
		}
		d.emitCheckRunError(spec.GetRunId(), 0, err)
		d.clearCheckRun(spec.GetRunId(), nil)
	}
}

func (d *agentDaemon) emitCheckRunError(runID string, seq int64, err error) {
	if err == nil {
		return
	}
	d.emitCheckRunLog(runID, &seq, err.Error())
	d.emitCheckRunTerminal(runID, &seq, "errored", -1, oneLineSummary(err.Error()))
}

func (d *agentDaemon) emitCheckRunRunning(runID string, seq *int64) {
	if strings.TrimSpace(runID) == "" {
		return
	}
	*seq++
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{
		RunId:     runID,
		Status:    "running",
		ClientSeq: *seq,
	})
}

func (d *agentDaemon) emitCheckRunLog(runID string, seq *int64, log string) {
	if strings.TrimSpace(runID) == "" || log == "" {
		return
	}
	*seq++
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{
		RunId:     runID,
		LogChunk:  log,
		Stream:    "stdout",
		ClientSeq: *seq,
	})
}

func (d *agentDaemon) emitCheckRunTerminal(runID string, seq *int64, status string, exitCode int32, summary string) {
	if strings.TrimSpace(runID) == "" {
		return
	}
	*seq++
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{
		RunId:     runID,
		Status:    status,
		ExitCode:  exitCode,
		ClientSeq: *seq,
		Final:     true,
		Summary:   summary,
	})
}

func (d *agentDaemon) sendCheckRunUpdate(update *corev1.CheckRunUpdate) {
	if update == nil {
		return
	}
	runID := strings.TrimSpace(update.GetRunId())
	if runID == "" {
		return
	}
	update.RunId = runID

	outbox := d.lockCurrentCheckRunOutboxForSend(runID)
	outbox.pending = append(outbox.pending, update)
	if update.GetFinal() {
		outbox.terminal = true
	}
	outbox.dropOverflowLocked()
	d.flushCheckRunPendingLocked(outbox)
	outbox.outMu.Unlock()

	d.enforceCheckRunOutboxLimit()
}

func (d *agentDaemon) lockCurrentCheckRunOutboxForSend(runID string) *checkRunOutbox {
	for {
		outbox := d.ensureCheckRunOutbox(runID)
		outbox.outMu.Lock()
		current := d.isCurrentCheckRunOutbox(runID, outbox)
		if current {
			return outbox
		}
		outbox.outMu.Unlock()
	}
}

func (d *agentDaemon) ensureCheckRunOutbox(runID string) *checkRunOutbox {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.checkRunOutboxes == nil {
		d.checkRunOutboxes = map[string]*checkRunOutbox{}
	}
	if outbox := d.checkRunOutboxes[runID]; outbox != nil {
		return outbox
	}
	outbox := &checkRunOutbox{}
	d.checkRunOutboxes[runID] = outbox
	d.checkRunOutboxOrder = append(d.checkRunOutboxOrder, runID)
	return outbox
}

func (d *agentDaemon) isCurrentCheckRunOutbox(runID string, outbox *checkRunOutbox) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.checkRunOutboxes[runID] == outbox
}

func (outbox *checkRunOutbox) dropOverflowLocked() {
	for len(outbox.pending) > agentMaxPendingCheckRunUpdates {
		drop := -1
		for i, update := range outbox.pending {
			if isLogOnlyCheckRunUpdate(update) {
				drop = i
				break
			}
		}
		if drop < 0 {
			return
		}
		copy(outbox.pending[drop:], outbox.pending[drop+1:])
		outbox.pending[len(outbox.pending)-1] = nil
		outbox.pending = outbox.pending[:len(outbox.pending)-1]
	}
}

func isLogOnlyCheckRunUpdate(update *corev1.CheckRunUpdate) bool {
	return update != nil && update.GetStatus() == "" && !update.GetFinal()
}

func (d *agentDaemon) flushCheckRunPendingLocked(outbox *checkRunOutbox) {
	if outbox == nil {
		return
	}
	d.mu.Lock()
	sq := d.sendQueue
	d.mu.Unlock()
	if sq == nil {
		return
	}
	if sq != outbox.flushQueue {
		outbox.flushQueue = sq
		outbox.flushedSeq = outbox.ackedClientSeq
	}
	for _, update := range outbox.pending {
		if update.GetClientSeq() <= outbox.flushedSeq {
			continue
		}
		msg := &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_CheckUpdate{CheckUpdate: update}}
		if err := sq.send(context.Background(), msg); err != nil {
			return
		}
		outbox.flushedSeq = update.GetClientSeq()
	}
}

func (d *agentDaemon) enforceCheckRunOutboxLimit() {
	for {
		d.mu.Lock()
		if len(d.checkRunOutboxes) <= agentMaxCheckRunOutboxes {
			d.mu.Unlock()
			return
		}
		order := append([]string(nil), d.checkRunOutboxOrder...)
		d.mu.Unlock()

		victim := d.oldestEvictableCheckRunOutbox(order)
		if victim == "" {
			return
		}
		d.mu.Lock()
		if d.checkRunOutboxes[victim] != nil {
			delete(d.checkRunOutboxes, victim)
			d.removeCheckRunOutboxOrderLocked(victim)
		}
		d.mu.Unlock()
	}
}

func (d *agentDaemon) oldestEvictableCheckRunOutbox(order []string) string {
	oldestTerminal := ""
	for _, runID := range order {
		d.mu.Lock()
		outbox := d.checkRunOutboxes[runID]
		d.mu.Unlock()
		if outbox == nil {
			continue
		}
		outbox.outMu.Lock()
		terminal := outbox.terminal
		stale := terminal && len(outbox.pending) == 0
		outbox.outMu.Unlock()
		if stale {
			return runID
		}
		if terminal && oldestTerminal == "" {
			oldestTerminal = runID
		}
	}
	return oldestTerminal
}

func (d *agentDaemon) removeCheckRunOutboxOrderLocked(runID string) {
	for i, existing := range d.checkRunOutboxOrder {
		if existing == runID {
			copy(d.checkRunOutboxOrder[i:], d.checkRunOutboxOrder[i+1:])
			d.checkRunOutboxOrder[len(d.checkRunOutboxOrder)-1] = ""
			d.checkRunOutboxOrder = d.checkRunOutboxOrder[:len(d.checkRunOutboxOrder)-1]
			return
		}
	}
}

func checkSpecFromProto(spec *corev1.CheckRunSpec) checks.CheckSpec {
	if spec == nil {
		return checks.CheckSpec{}
	}
	timeout := time.Duration(0)
	if spec.GetTimeoutMs() > 0 {
		timeout = time.Duration(spec.GetTimeoutMs()) * time.Millisecond
	}
	return checks.CheckSpec{
		Name:             spec.GetName(),
		Run:              spec.GetCommand(),
		Image:            spec.GetImage(),
		Setup:            append([]string(nil), spec.GetSetup()...),
		Cache:            append([]string(nil), spec.GetCache()...),
		WorkingDir:       spec.GetWorkingDir(),
		MaterializePaths: append([]string(nil), spec.GetMaterializePaths()...),
		Env:              copyCheckEnv(spec.GetEnv()),
		Network:          spec.GetNetwork(),
		Memory:           spec.GetMemory(),
		CPUs:             spec.GetCpus(),
		Timeout:          timeout,
	}
}

func copyCheckEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (d *agentDaemon) startCheckRun(runID string, cancel context.CancelFunc) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.checkRuns == nil {
		d.checkRuns = map[string]context.CancelFunc{}
	}
	if d.checkRuns[runID] != nil {
		return false
	}
	if _, ok := d.recentCheckRuns[runID]; ok {
		return false
	}
	d.checkRuns[runID] = cancel
	return true
}

func (d *agentDaemon) clearCheckRun(runID string, cancel context.CancelFunc) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.checkRuns, runID)
	d.rememberRecentCheckRunLocked(runID)
}

func (d *agentDaemon) rememberRecentCheckRunLocked(runID string) {
	if d.recentCheckRuns == nil {
		d.recentCheckRuns = map[string]struct{}{}
	}
	if _, ok := d.recentCheckRuns[runID]; ok {
		for i, existing := range d.recentCheckRunOrder {
			if existing == runID {
				copy(d.recentCheckRunOrder[i:], d.recentCheckRunOrder[i+1:])
				d.recentCheckRunOrder = d.recentCheckRunOrder[:len(d.recentCheckRunOrder)-1]
				break
			}
		}
	}
	d.recentCheckRuns[runID] = struct{}{}
	d.recentCheckRunOrder = append(d.recentCheckRunOrder, runID)
	for len(d.recentCheckRunOrder) > agentRecentCheckRunLimit {
		oldest := d.recentCheckRunOrder[0]
		copy(d.recentCheckRunOrder, d.recentCheckRunOrder[1:])
		d.recentCheckRunOrder = d.recentCheckRunOrder[:len(d.recentCheckRunOrder)-1]
		delete(d.recentCheckRuns, oldest)
	}
}

func (d *agentDaemon) handleCancelCheckRun(cancel *corev1.CancelCheckRun) {
	if cancel == nil {
		return
	}
	runID := strings.TrimSpace(cancel.GetRunId())
	if runID == "" {
		return
	}
	d.mu.Lock()
	cancelFn := d.checkRuns[runID]
	d.mu.Unlock()
	if cancelFn != nil {
		cancelFn()
	}
}

func (d *agentDaemon) handleCheckRunAck(ack *corev1.CheckRunAck) {
	if ack == nil {
		return
	}
	runID := strings.TrimSpace(ack.GetRunId())
	if runID == "" {
		return
	}
	d.mu.Lock()
	outbox := d.checkRunOutboxes[runID]
	d.mu.Unlock()
	if outbox == nil {
		return
	}

	outbox.outMu.Lock()
	if ack.GetAckedClientSeq() > outbox.ackedClientSeq {
		outbox.ackedClientSeq = ack.GetAckedClientSeq()
	}
	kept := outbox.pending[:0]
	for _, update := range outbox.pending {
		if update.GetClientSeq() > outbox.ackedClientSeq {
			kept = append(kept, update)
		}
	}
	for i := len(kept); i < len(outbox.pending); i++ {
		outbox.pending[i] = nil
	}
	outbox.pending = kept
	if len(outbox.pending) == 0 && outbox.terminal {
		d.mu.Lock()
		if d.checkRunOutboxes[runID] == outbox {
			delete(d.checkRunOutboxes, runID)
			d.removeCheckRunOutboxOrderLocked(runID)
		}
		d.mu.Unlock()
	}
	outbox.outMu.Unlock()
}

func (d *agentDaemon) checkBaseContext() context.Context {
	if d.baseCtx != nil {
		return d.baseCtx
	}
	return context.Background()
}

func detectContainerRuntimes() []string {
	var out []string
	for _, name := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(name); err == nil {
			out = append(out, name)
		}
	}
	return out
}

type checkTreeRepoClient struct {
	inner   RepoClient
	account string
}

func (c checkTreeRepoClient) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest, opts ...grpc.CallOption) (*corev1.ListDirectoryResponse, error) {
	mapped := &corev1.ListDirectoryRequest{
		CommitId:   req.GetCommitId(),
		Path:       c.storagePath(req.GetPath()),
		Cursor:     req.GetCursor(),
		PageSize:   req.GetPageSize(),
		Slice:      req.GetSlice(),
		RootTreeId: req.GetRootTreeId(),
	}
	res, err := c.inner.ListDirectory(ctx, mapped, opts...)
	if err != nil {
		return nil, err
	}
	out := &corev1.ListDirectoryResponse{NextCursor: res.GetNextCursor()}
	for _, entry := range res.GetEntries() {
		if entry == nil {
			continue
		}
		out.Entries = append(out.Entries, &corev1.TreeEntry{
			Path:          c.logicalPath(entry.GetPath()),
			Name:          entry.GetName(),
			Kind:          entry.GetKind(),
			Mode:          entry.GetMode(),
			TreeId:        entry.GetTreeId(),
			BlobId:        entry.GetBlobId(),
			SymlinkTarget: entry.GetSymlinkTarget(),
			Size:          entry.GetSize(),
			ContentHash:   entry.GetContentHash(),
		})
	}
	return out, nil
}

func (c checkTreeRepoClient) ReadFile(ctx context.Context, req *corev1.ReadFileRequest, opts ...grpc.CallOption) (*corev1.ReadFileResponse, error) {
	mapped := &corev1.ReadFileRequest{
		CommitId:   req.GetCommitId(),
		Path:       c.storagePath(req.GetPath()),
		Offset:     req.GetOffset(),
		Length:     req.GetLength(),
		RootTreeId: req.GetRootTreeId(),
		Slice:      req.GetSlice(),
	}
	return c.inner.ReadFile(ctx, mapped, opts...)
}

func (c checkTreeRepoClient) storagePath(logicalPath string) string {
	account := strings.Trim(strings.TrimSpace(c.account), "/")
	if account == "" {
		return logicalAbsPath(logicalPath)
	}
	cleaned := logicalAbsPath(logicalPath)
	if cleaned == "/" {
		return "/" + account
	}
	return "/" + path.Join(account, strings.TrimPrefix(cleaned, "/"))
}

func (c checkTreeRepoClient) logicalPath(storagePath string) string {
	account := strings.Trim(strings.TrimSpace(c.account), "/")
	cleaned := logicalAbsPath(storagePath)
	if account == "" {
		return cleaned
	}
	accountRoot := "/" + account
	switch {
	case cleaned == accountRoot:
		return "/"
	case strings.HasPrefix(cleaned, accountRoot+"/"):
		return "/" + strings.TrimPrefix(cleaned, accountRoot+"/")
	default:
		return cleaned
	}
}
