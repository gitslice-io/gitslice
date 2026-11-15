package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/diffutil"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type workspaceDiffOutput struct {
	Workspace        string   `json:"workspace"`
	BaseCommitID     string   `json:"base_commit_id"`
	ChangedPathCount int      `json:"changed_path_count"`
	ChangedPaths     []string `json:"changed_paths"`
	Diff             string   `json:"diff,omitempty"`
}

func (r Runner) runDiff(ctx context.Context, opts commandOptions, commitArgs []string, from, to string, nameOnly, stat bool) error {
	if len(commitArgs) > 0 {
		if strings.TrimSpace(from) != "" || strings.TrimSpace(to) != "" {
			return userError("invalid_args", "commit ids cannot be combined with --from or --to", "Use gs diff <commit> [commit] or gs diff --from <patchset> --to <patchset>.")
		}
		return r.runCommitDiff(ctx, opts, commitArgs, nameOnly, stat)
	}
	if strings.TrimSpace(from) != "" || strings.TrimSpace(to) != "" {
		return r.runChangesetDiff(ctx, opts, "", "", from, to, nameOnly, stat)
	}
	return r.runWorkspaceDiff(ctx, opts, nameOnly, stat)
}

func (r Runner) runCommitDiff(ctx context.Context, opts commandOptions, commitArgs []string, nameOnly, stat bool) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	oldInput := ""
	newInput := commitArgs[0]
	if len(commitArgs) == 2 {
		oldInput = commitArgs[0]
		newInput = commitArgs[1]
	}
	oldCommit, newCommit, changed, diffText, err := r.commitDiff(ctx, cfg, conn, oldInput, newInput)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"from_commit_id": oldCommitID(oldCommit),
			"to_commit_id":   newCommit.Id,
			"changed_paths":  changed,
			"diff":           diffText,
		})
	}
	if opts.Quiet {
		return nil
	}
	color := r.colorEnabled(opts)
	if nameOnly {
		printPathListColor(r.Stdout, changed, color)
		return nil
	}
	if stat {
		printPathStatColor(r.Stdout, changed, color)
		return nil
	}
	_, err = io.WriteString(r.Stdout, colorUnifiedDiff(compactUnifiedDiff(diffText, opts.diffContext()), color))
	return err
}

func (r Runner) runWorkspaceDiff(ctx context.Context, opts commandOptions, nameOnly, stat bool) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	// Lazily: --stat and --name-only work from local state, offline too.
	conn, err := dialLazy(cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	found, err := r.loadWorkspaceEdits(ctx, conn, cfg, ws, state)
	if err != nil {
		return err
	}
	base := found.base
	baseCommitID := state.BaseCommitID
	if baseCommitID == "" {
		baseCommitID = base.CommitID
	}
	if baseCommitID == "" {
		baseCommitID = ws.BaseCommitID
	}
	if baseCommitID == "" {
		return userError("invalid_workspace_state", "workspace has no base commit", "Run gs init <slice|account:slice> again.")
	}
	edits, current := found.edits, found.current
	changed := changedPathsFromEdits(edits)
	if opts.jsonOutput() {
		diffText, err := r.workspaceDiffText(ctx, conn, cfg, ws, base, baseCommitID, current, changed)
		if err != nil {
			return err
		}
		return r.writeJSONOutput(opts, workspaceDiffOutput{
			Workspace:        ws.Account + ":" + ws.Slice,
			BaseCommitID:     baseCommitID,
			ChangedPathCount: len(changed),
			ChangedPaths:     changed,
			Diff:             diffText,
		})
	}
	if opts.Quiet {
		return nil
	}
	if nameOnly {
		printPathListColor(r.Stdout, changed, r.colorEnabled(opts))
		return nil
	}
	if stat {
		cache, _ := r.objectCache()
		changes := summarizeChanges(edits, base, current, cache)
		fmt.Fprintln(r.Stdout, changeHeadline(changes))
		printChangeSummary(r.Stdout, changes, "  ", r.colorEnabled(opts))
		return nil
	}
	diffText, err := r.workspaceDiffText(ctx, conn, cfg, ws, base, baseCommitID, current, changed)
	if err != nil {
		return err
	}
	_, err = io.WriteString(r.Stdout, colorUnifiedDiff(compactUnifiedDiff(diffText, opts.diffContext()), r.colorEnabled(opts)))
	return err
}

func (r Runner) workspaceDiffText(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, base BaseSnapshot, baseCommitID string, current map[string]workingFile, changed []string) (string, error) {
	if len(changed) == 0 {
		return "", nil
	}
	if conn == nil {
		var err error
		conn, err = dial(ctx, cfg.ServerAddr)
		if err != nil {
			return "", err
		}
		defer conn.Close()
	}
	repo := corev1.NewRepositoryServiceClient(conn)
	blob := corev1.NewBlobServiceClient(conn)
	cache, err := r.objectCache()
	if err != nil {
		return "", err
	}
	callCtx := authContext(ctx, cfg)
	sliceRef := &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}
	var prefetch []remoteReadTarget
	for _, p := range changed {
		if file, ok := base.Files[p]; ok && file.ContentHash != "" {
			if _, cachedOK, _ := readVerifiedCachedBytes(cache, file.ContentHash); !cachedOK {
				prefetch = append(prefetch, remoteReadTarget{
					Path:        p,
					ContentHash: file.ContentHash,
					Size:        file.Size,
				})
			}
		}
	}
	if len(prefetch) > 0 {
		if _, err := prefetchRemoteFilesToCache(callCtx, repo, blob, sliceRef, cache, baseCommitID, prefetch); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	for _, p := range changed {
		oldFile, err := r.workspaceBaseDiffFile(callCtx, repo, blob, sliceRef, cache, base, baseCommitID, p)
		if err != nil {
			return "", err
		}
		newFile := diffutil.File{Path: p}
		if file, ok := current[p]; ok {
			data, err := readWorkingFile(file, cache)
			if err != nil {
				return "", err
			}
			newFile = diffutil.File{Path: p, Exists: true, Data: data}
		}
		b.WriteString(diffutil.UnifiedFileDiff(oldFile, newFile))
	}
	return b.String(), nil
}

func (r Runner) commitDiffText(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, oldInput, newInput string) (string, error) {
	_, _, _, diffText, err := r.commitDiff(ctx, cfg, conn, oldInput, newInput)
	return diffText, err
}

func (r Runner) commitDiff(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, oldInput, newInput string) (*corev1.Commit, *corev1.Commit, []string, string, error) {
	repo := corev1.NewRepositoryServiceClient(conn)
	newCommit, err := r.resolveCommit(ctx, cfg, conn, newInput, nil)
	if err != nil {
		return nil, nil, nil, "", err
	}
	var oldCommit *corev1.Commit
	explicitOld := strings.TrimSpace(oldInput) != ""
	if explicitOld {
		oldCommit, err = r.resolveCommit(ctx, cfg, conn, oldInput, nil)
		if err != nil {
			return nil, nil, nil, "", err
		}
	} else if len(newCommit.ParentIds) > 0 {
		oldCommit, err = repo.GetCommit(authContext(ctx, cfg), &corev1.GetCommitRequest{CommitId: newCommit.ParentIds[0]})
		if err != nil {
			return nil, nil, nil, "", err
		}
	}
	changed := commitChangedPaths(newCommit)
	if explicitOld {
		changed = commitDiffChangedPaths(oldCommit, newCommit)
	}
	callCtx := authContext(ctx, cfg)
	blob := corev1.NewBlobServiceClient(conn)
	cache, _ := r.objectCache()
	oldFiles, err := batchReadCommitDiffFiles(callCtx, repo, blob, cache, oldCommit, changed)
	if err != nil {
		return nil, nil, nil, "", err
	}
	newFiles, err := batchReadCommitDiffFiles(callCtx, repo, blob, cache, newCommit, changed)
	if err != nil {
		return nil, nil, nil, "", err
	}
	var b strings.Builder
	for _, p := range changed {
		b.WriteString(diffutil.UnifiedFileDiff(oldFiles[p], newFiles[p]))
	}
	return oldCommit, newCommit, changed, b.String(), nil
}

func (r Runner) commitDiffFile(ctx context.Context, repo corev1.RepositoryServiceClient, commit *corev1.Commit, p string) (diffutil.File, error) {
	if commit == nil || commit.Id == "" {
		return diffutil.File{Path: p}, nil
	}
	read, err := repo.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: commit.Id, Path: p})
	if grpcstatus.Code(err) == codes.NotFound {
		return diffutil.File{Path: p}, nil
	}
	if err != nil {
		return diffutil.File{}, err
	}
	return diffutil.File{Path: p, Exists: true, Data: read.Data}, nil
}

func commitDiffChangedPaths(oldCommit, newCommit *corev1.Commit) []string {
	seen := map[string]struct{}{}
	for _, commit := range []*corev1.Commit{oldCommit, newCommit} {
		if commit == nil {
			continue
		}
		for _, p := range commit.ChangedPaths {
			if strings.TrimSpace(p) == "" {
				continue
			}
			seen[p] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func commitChangedPaths(commit *corev1.Commit) []string {
	if commit == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, p := range commit.ChangedPaths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		seen[p] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func oldCommitID(commit *corev1.Commit) string {
	if commit == nil {
		return ""
	}
	return commit.Id
}

func (r Runner) workspaceBaseDiffFile(ctx context.Context, repo corev1.RepositoryServiceClient, blob corev1.BlobServiceClient, sliceRef *corev1.SliceRef, cache *clientcache.ObjectCache, base BaseSnapshot, baseCommitID, p string) (diffutil.File, error) {
	var expectedHash string
	if file, ok := base.Files[p]; ok && file.ContentHash != "" {
		expectedHash = file.ContentHash
		if cache != nil && cache.Exists(file.ContentHash) {
			data, ok, err := readVerifiedCachedBytes(cache, file.ContentHash)
			if err != nil {
				return diffutil.File{}, err
			}
			if ok {
				return diffutil.File{Path: p, Exists: true, Data: data}, nil
			}
		}
		if blob != nil && sliceRef != nil {
			data, err := readBlobStreamBytes(ctx, blob, sliceRef, file.ContentHash, 0, 0)
			if err == nil {
				if _, verifyErr := putVerifiedCachedBytes(cache, p, file.ContentHash, data); verifyErr != nil {
					return diffutil.File{}, verifyErr
				}
				return diffutil.File{Path: p, Exists: true, Data: data}, nil
			}
			if grpcstatus.Code(err) != codes.NotFound && grpcstatus.Code(err) != codes.Unimplemented {
				return diffutil.File{}, err
			}
		}
	}
	data, ok, err := fetchSingleRemoteTarget(ctx, repo, blob, sliceRef, cache, baseCommitID, remoteReadTarget{
		Path:        p,
		ContentHash: expectedHash,
	})
	if err != nil {
		return diffutil.File{}, err
	}
	if !ok {
		return diffutil.File{Path: p}, nil
	}
	return diffutil.File{Path: p, Exists: true, Data: data}, nil
}
