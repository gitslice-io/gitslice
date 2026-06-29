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
	"time"

	"github.com/gitslice-io/gitslice/internal/checkexec"
	"github.com/gitslice-io/gitslice/internal/checks"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

const checkMaterializePageSize = 1000

type RepoClient interface {
	ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest, opts ...grpc.CallOption) (*corev1.ListDirectoryResponse, error)
	ReadFile(ctx context.Context, req *corev1.ReadFileRequest, opts ...grpc.CallOption) (*corev1.ReadFileResponse, error)
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
	for _, prefix := range prefixes {
		if prefix == "/" {
			if err := d.materializeCheckDirectory(ctx, repo, rootTreeID, "/", destRoot); err != nil {
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
			if err := d.materializeCheckDirectory(ctx, repo, rootTreeID, entryPath, destRoot); err != nil {
				return err
			}
		case corev1.EntryKind_ENTRY_KIND_FILE:
			if err := materializeCheckFile(ctx, repo, rootTreeID, entry, destRoot); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported check tree entry kind for %s: %s", prefix, entryKindName(entry.GetKind()))
		}
	}
	return nil
}

func (d *agentDaemon) materializeCheckDirectory(ctx context.Context, repo RepoClient, rootTreeID, dir, destRoot string) error {
	dir, err := normalizeCheckMaterializePath(dir)
	if err != nil {
		return err
	}
	target, err := checkTreeLocalPath(destRoot, dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
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
			if err := d.materializeCheckDirectory(ctx, repo, rootTreeID, entryPath, destRoot); err != nil {
				return err
			}
		case corev1.EntryKind_ENTRY_KIND_FILE:
			if err := materializeCheckFile(ctx, repo, rootTreeID, entry, destRoot); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported check tree entry kind for %s: %s", entry.GetPath(), entryKindName(entry.GetKind()))
		}
	}
	return nil
}

func materializeCheckFile(ctx context.Context, repo RepoClient, rootTreeID string, entry *corev1.TreeEntry, destRoot string) error {
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
	read, err := repo.ReadFile(ctx, &corev1.ReadFileRequest{
		RootTreeId: rootTreeID,
		Path:       logicalAbsPath(entryPath),
	})
	if err != nil {
		return err
	}
	mode := entry.GetMode()
	if mode == 0 {
		mode = 0o100644
	}
	_, err = writeWorkspaceFile(target, mode, bytes.NewReader(read.GetData()))
	return err
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

	tempDir, err := os.MkdirTemp("", "gitslice-check-*")
	if err != nil {
		d.emitCheckRunError(spec.GetRunId(), 0, err)
		return
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			fmt.Fprintf(d.runner.stderr(), "check workspace remove failed for %s: %v\n", tempDir, err)
		}
	}()

	seq := int64(0)
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
		d.emitCheckRunError(spec.GetRunId(), 0, err)
	}
}

func (d *agentDaemon) emitCheckRunError(runID string, seq int64, err error) {
	if err == nil {
		return
	}
	d.emitCheckRunLog(runID, &seq, err.Error())
	d.emitCheckRunTerminal(runID, &seq, "errored", -1, oneLineSummary(err.Error()))
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
	_ = d.sendDaemonMessage(context.Background(), &corev1.DaemonMessage{
		Payload: &corev1.DaemonMessage_CheckUpdate{CheckUpdate: update},
	})
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
		WorkingDir:       spec.GetWorkingDir(),
		MaterializePaths: append([]string(nil), spec.GetMaterializePaths()...),
		Env:              copyCheckEnv(spec.GetEnv()),
		Network:          spec.GetNetwork(),
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

func (d *agentDaemon) handleCheckRunAck(_ *corev1.CheckRunAck) {}

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
