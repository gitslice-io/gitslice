package gitcompat

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/status"
)

const zeroGitOID = "0000000000000000000000000000000000000000"

type receivePackRequest struct {
	commands     []receivePackCommand
	capabilities map[string]struct{}
	packfile     []byte
}

type receivePackCommand struct {
	OldOID string
	NewOID string
	Ref    string
}

type receivePackResult struct {
	UnpackStatus string
	RefStatuses  []receivePackRefStatus
	Messages     []string
}

type receivePackRefStatus struct {
	Ref     string
	OK      bool
	Message string
}

type changesetPushTarget struct {
	New      bool
	Selector string
}

type pushedGitFile struct {
	Path      string
	GitPath   string
	GitBlobID string
	Mode      uint32
	Data      []byte
}

func writeReceivePackAdvertisement(w http.ResponseWriter, projection *Projection) {
	w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
	var out bytes.Buffer
	appendPktLine(&out, []byte("# service=git-receive-pack\n"))
	appendFlush(&out)
	line := fmt.Sprintf("%s refs/heads/main\x00report-status side-band-64k agent=gitslice\n", projection.GitCommitID)
	appendPktLine(&out, []byte(line))
	appendFlush(&out)
	_, _ = w.Write(out.Bytes())
}

func parseReceivePackRequest(data []byte) (*receivePackRequest, error) {
	var commands []receivePackCommand
	caps := map[string]struct{}{}
	offset := 0
	first := true
	for {
		payload, next, flush, err := readPktLine(data, offset)
		if err != nil {
			return nil, err
		}
		offset = next
		if flush {
			break
		}
		line := strings.TrimSuffix(string(payload), "\n")
		if first {
			first = false
			if command, capabilities, ok := strings.Cut(line, "\x00"); ok {
				line = command
				for _, cap := range strings.Fields(capabilities) {
					caps[cap] = struct{}{}
				}
			}
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed receive-pack command %q", line)
		}
		commands = append(commands, receivePackCommand{
			OldOID: fields[0],
			NewOID: fields[1],
			Ref:    fields[2],
		})
	}
	return &receivePackRequest{commands: commands, capabilities: caps, packfile: data[offset:]}, nil
}

func readPktLine(data []byte, offset int) ([]byte, int, bool, error) {
	if offset+4 > len(data) {
		return nil, offset, false, errors.New("truncated pkt-line header")
	}
	n, err := strconv.ParseInt(string(data[offset:offset+4]), 16, 32)
	if err != nil {
		return nil, offset, false, fmt.Errorf("invalid pkt-line length %q", string(data[offset:offset+4]))
	}
	offset += 4
	if n == 0 {
		return nil, offset, true, nil
	}
	if n < 4 {
		return nil, offset, false, fmt.Errorf("invalid pkt-line length %d", n)
	}
	end := offset + int(n) - 4
	if end > len(data) {
		return nil, offset, false, errors.New("truncated pkt-line payload")
	}
	return data[offset:end], end, false, nil
}

func (h *Handler) applyReceivePack(ctx, serviceCtx context.Context, repoPath string, projection *Projection, account, slice string, req *receivePackRequest) receivePackResult {
	if len(req.commands) == 0 {
		return receivePackResult{UnpackStatus: "ok", Messages: []string{"No ref update commands were sent.\n"}}
	}
	if len(req.commands) != 1 {
		statuses := make([]receivePackRefStatus, 0, len(req.commands))
		for _, cmd := range req.commands {
			statuses = append(statuses, receivePackRefStatus{
				Ref:     cmd.Ref,
				Message: "Gitslice MVP accepts exactly one ref update per Git push",
			})
		}
		return receivePackResult{UnpackStatus: "ok", RefStatuses: statuses}
	}
	cmd := req.commands[0]
	target, err := parseChangesetPushTarget(cmd.Ref, account, slice)
	if err != nil {
		return rejectedReceivePack(cmd.Ref, err.Error())
	}
	if isZeroGitOID(cmd.NewOID) {
		return rejectedReceivePack(cmd.Ref, "deleting Git refs is not supported; push commits to refs/changes/new or refs/changes/<changeset>")
	}
	if len(req.packfile) == 0 && cmd.NewOID != projection.GitCommitID {
		return receivePackResult{
			UnpackStatus: "error missing packfile",
			RefStatuses:  []receivePackRefStatus{{Ref: cmd.Ref, Message: "missing packfile for pushed commit"}},
		}
	}

	pushRepo, cleanup, err := indexPushPack(ctx, repoPath, req.packfile)
	if err != nil {
		return receivePackResult{
			UnpackStatus: "error " + userFacingError(err),
			RefStatuses:  []receivePackRefStatus{{Ref: cmd.Ref, Message: "could not unpack pushed Git objects"}},
		}
	}
	defer cleanup()

	sliceRef := &corev1.SliceRef{Account: account, Slice: slice}
	edits, title, err := pushedCommitFileEdits(ctx, serviceCtx, pushRepo, projection.GitCommitID, cmd.NewOID, sliceRef, h.blobs)
	if err != nil {
		return rejectedReceivePack(cmd.Ref, userFacingError(err))
	}
	if len(edits) == 0 {
		return rejectedReceivePack(cmd.Ref, "push does not change any files relative to the current projected head")
	}
	if err := h.ensurePushEditsContained(ctx, sliceRef, edits); err != nil {
		return rejectedReceivePack(cmd.Ref, userFacingError(err))
	}

	if target.New {
		cs, err := h.changesets.CreateChangeset(serviceCtx, &corev1.CreateChangesetRequest{
			AuthoringSlice: sliceRef,
			TargetRef:      storage.DefaultTargetRef,
			BaseCommitId:   projection.NativeCommitID,
			Title:          title,
			Description:    "Created from git push to refs/changes/new.",
		})
		if err != nil {
			return rejectedReceivePack(cmd.Ref, userFacingError(err))
		}
		patchset, err := h.changesets.UpdateChangeset(serviceCtx, &corev1.UpdateChangesetRequest{
			ChangesetId:  cs.Id,
			BaseCommitId: projection.NativeCommitID,
			FileEdits:    edits,
		})
		if err != nil {
			return rejectedReceivePack(cmd.Ref, userFacingError(err))
		}
		changesetLabel := firstNonEmpty(storage.ShortChangesetID(cs.Id), cs.Id)
		patchsetLabel := patchsetPushLabel(cs.Id, patchset)
		return acceptedReceivePack(cmd.Ref, fmt.Sprintf("Created changeset %s patchset %s\n", changesetLabel, patchsetLabel))
	}

	cs, err := h.changesets.GetChangeset(serviceCtx, &corev1.GetChangesetRequest{ChangesetId: target.Selector})
	if err != nil {
		return rejectedReceivePack(cmd.Ref, userFacingError(err))
	}
	if cs.AuthoringSlice == nil || cs.AuthoringSlice.Account != account || cs.AuthoringSlice.Slice != slice {
		return rejectedReceivePack(cmd.Ref, fmt.Sprintf("changeset %s does not belong to slice %s/%s", target.Selector, account, slice))
	}
	patchset, err := h.changesets.UpdateChangeset(serviceCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: cs.CurrentPatchsetId,
		BaseCommitId:              projection.NativeCommitID,
		FileEdits:                 edits,
	})
	if err != nil {
		return rejectedReceivePack(cmd.Ref, userFacingError(err))
	}
	changesetLabel := firstNonEmpty(storage.ShortChangesetID(cs.Id), cs.Id)
	patchsetLabel := patchsetPushLabel(cs.Id, patchset)
	return acceptedReceivePack(cmd.Ref, fmt.Sprintf("Updated changeset %s patchset %s\n", changesetLabel, patchsetLabel))
}

func patchsetPushLabel(changesetID string, patchset *corev1.Patchset) string {
	if patchset == nil {
		return ""
	}
	changesetLabel := storage.ShortChangesetID(changesetID)
	if changesetLabel != "" && patchset.Number > 0 {
		return fmt.Sprintf("%s.%d", changesetLabel, patchset.Number)
	}
	return patchset.Id
}

func (h *Handler) ensurePushEditsContained(ctx context.Context, sliceRef *corev1.SliceRef, edits []*corev1.FileEdit) error {
	slice, err := h.projector.slices.Resolve(ctx, sliceRef)
	if err != nil {
		return err
	}
	for _, edit := range edits {
		for _, p := range []string{edit.Path, edit.OldPath} {
			if p == "" {
				continue
			}
			if !paths.InAnyPrefix(slice.Definition.IncludedPaths, p) {
				return fmt.Errorf("path %s is outside slice %s/%s", p, sliceRef.Account, sliceRef.Slice)
			}
		}
	}
	return nil
}

func parseChangesetPushTarget(ref, account, slice string) (changesetPushTarget, error) {
	if ref == "refs/changes/new" {
		return changesetPushTarget{New: true}, nil
	}
	const prefix = "refs/changes/"
	if strings.HasPrefix(ref, prefix) {
		selector := strings.TrimPrefix(ref, prefix)
		if selector == "" || strings.Contains(selector, "/") {
			return changesetPushTarget{}, fmt.Errorf("push to %s is not supported; use refs/changes/new or refs/changes/<changeset-id>", ref)
		}
		return changesetPushTarget{Selector: selector}, nil
	}
	return changesetPushTarget{}, fmt.Errorf("direct Git pushes to %s are protected; push to HEAD:refs/changes/new to create a Gitslice changeset", ref)
}

func indexPushPack(ctx context.Context, projectedRepoPath string, packfile []byte) (string, func(), error) {
	tempRoot, err := os.MkdirTemp(filepath.Dir(projectedRepoPath), ".push-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tempRoot) }
	repoPath := filepath.Join(tempRoot, "repo.git")
	if err := runGit(ctx, "", nil, "init", "--bare", repoPath); err != nil {
		cleanup()
		return "", nil, err
	}
	infoDir := filepath.Join(repoPath, "objects", "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		cleanup()
		return "", nil, err
	}
	alternates := filepath.Join(projectedRepoPath, "objects") + "\n"
	if err := os.WriteFile(filepath.Join(infoDir, "alternates"), []byte(alternates), 0o644); err != nil {
		cleanup()
		return "", nil, err
	}
	if len(packfile) > 0 {
		if _, err := gitOutputInput(ctx, repoPath, nil, packfile, "index-pack", "--stdin", "--fix-thin"); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return repoPath, cleanup, nil
}

func pushedCommitFileEdits(ctx, serviceCtx context.Context, repoDir, baseCommitID, newCommitID string, sliceRef *corev1.SliceRef, blobs BlobAPI) ([]*corev1.FileEdit, string, error) {
	if ok, err := gitObjectExists(ctx, repoDir, newCommitID+"^{commit}"); err != nil {
		return nil, "", err
	} else if !ok {
		return nil, "", fmt.Errorf("pushed object %s is not a commit", shortGitOID(newCommitID))
	}
	if err := validateLinearPushHistory(ctx, repoDir, baseCommitID, newCommitID); err != nil {
		return nil, "", err
	}
	title, err := gitOutput(ctx, repoDir, nil, "log", "-1", "--format=%s", newCommitID)
	if err != nil {
		return nil, "", err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Git push " + shortGitOID(newCommitID)
	}
	raw, err := gitOutputBytes(ctx, repoDir, nil, "diff-tree", "-r", "-z", "--no-commit-id", "--name-status", "--find-renames", baseCommitID, newCommitID)
	if err != nil {
		return nil, "", err
	}
	deleted, upsertGitPaths, err := parseGitDiffNameStatus(raw)
	if err != nil {
		return nil, "", err
	}
	snapshot, err := gitFilesAtPaths(ctx, repoDir, newCommitID, upsertGitPaths)
	if err != nil {
		return nil, "", err
	}
	edits := make([]*corev1.FileEdit, 0, len(deleted)+len(upsertGitPaths))
	foundUpserts := make(map[string]struct{}, len(snapshot))
	for _, file := range snapshot {
		foundUpserts[file.GitPath] = struct{}{}
	}
	for _, gitPath := range deleted {
		globalPath, err := paths.FromWorkspacePath("/", gitPath)
		if err != nil {
			return nil, "", err
		}
		edits = append(edits, &corev1.FileEdit{Op: "delete", Path: globalPath})
	}
	for _, gitPath := range upsertGitPaths {
		file, ok := snapshot[gitPath]
		if !ok {
			if _, seen := foundUpserts[gitPath]; seen {
				continue
			}
			globalPath, err := paths.FromWorkspacePath("/", gitPath)
			if err != nil {
				return nil, "", err
			}
			edits = append(edits, &corev1.FileEdit{Op: "delete", Path: globalPath})
			continue
		}
		upload, err := blobs.UploadBlob(serviceCtx, &corev1.UploadBlobRequest{
			Data:  file.Data,
			Slice: sliceRef,
		})
		if err != nil {
			return nil, "", err
		}
		edits = append(edits, &corev1.FileEdit{
			Op:          "upsert",
			Path:        file.Path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        file.Mode,
		})
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].Path == edits[j].Path {
			return edits[i].Op < edits[j].Op
		}
		return edits[i].Path < edits[j].Path
	})
	return edits, title, nil
}

func validateLinearPushHistory(ctx context.Context, repoDir, baseCommitID, newCommitID string) error {
	if baseCommitID == newCommitID {
		return fmt.Errorf("push contains no commits beyond the current projected head")
	}
	ancestor, err := gitMergeBaseIsAncestor(ctx, repoDir, baseCommitID, newCommitID)
	if err != nil {
		return err
	}
	if !ancestor {
		return fmt.Errorf("NEEDS_REBASE: pushed commits are not based on the current projected main; run git fetch origin main and rebase before pushing to refs/changes/new")
	}
	raw, err := gitOutput(ctx, repoDir, nil, "rev-list", "--parents", "--reverse", baseCommitID+".."+newCommitID)
	if err != nil {
		return err
	}
	prev := baseCommitID
	count := 0
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		count++
		if len(fields) > 2 {
			return fmt.Errorf("merge commits are not supported by Git push into changesets; rebase to a single linear commit chain")
		}
		if len(fields) != 2 || fields[1] != prev {
			return fmt.Errorf("only single linear commit chains based on the current projected head are supported")
		}
		prev = fields[0]
	}
	if count == 0 {
		return fmt.Errorf("push contains no commits beyond the current projected head")
	}
	return nil
}

func gitFilesAtPaths(ctx context.Context, repoDir, commitID string, gitPaths []string) (map[string]pushedGitFile, error) {
	snapshot := map[string]pushedGitFile{}
	if len(gitPaths) == 0 {
		return snapshot, nil
	}
	const chunkSize = 512
	for start := 0; start < len(gitPaths); start += chunkSize {
		end := start + chunkSize
		if end > len(gitPaths) {
			end = len(gitPaths)
		}
		args := []string{"ls-tree", "-r", "-z", "--full-tree", commitID, "--"}
		for _, p := range gitPaths[start:end] {
			args = append(args, ":(literal)"+p)
		}
		raw, err := gitOutputBytes(ctx, repoDir, nil, args...)
		if err != nil {
			return nil, err
		}
		chunk, err := gitSnapshotFromTreeRecords(ctx, repoDir, raw)
		if err != nil {
			return nil, err
		}
		for p, file := range chunk {
			snapshot[p] = file
		}
	}
	return snapshot, nil
}

func gitSnapshotFromTreeRecords(ctx context.Context, repoDir string, raw []byte) (map[string]pushedGitFile, error) {
	snapshot := map[string]pushedGitFile{}
	blobIDs := make([]string, 0)
	for _, record := range bytes.Split(raw, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, gitPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("malformed ls-tree record %q", string(record))
		}
		fields := strings.Fields(string(meta))
		if len(fields) < 3 {
			return nil, fmt.Errorf("malformed ls-tree record %q", string(record))
		}
		if fields[1] != "blob" {
			return nil, fmt.Errorf("unsupported Git object type %s at %s", fields[1], string(gitPath))
		}
		modeValue, err := strconv.ParseUint(fields[0], 8, 32)
		if err != nil {
			return nil, err
		}
		pathText := string(gitPath)
		globalPath, err := paths.FromWorkspacePath("/", pathText)
		if err != nil {
			return nil, err
		}
		snapshot[pathText] = pushedGitFile{
			Path:      globalPath,
			GitPath:   pathText,
			GitBlobID: fields[2],
			Mode:      uint32(modeValue),
		}
		blobIDs = append(blobIDs, fields[2])
	}
	blobs, err := gitBlobContents(ctx, repoDir, blobIDs)
	if err != nil {
		return nil, err
	}
	for gitPath, file := range snapshot {
		data, ok := blobs[file.GitBlobID]
		if !ok {
			return nil, fmt.Errorf("missing Git blob %s for %s", file.GitBlobID, file.GitPath)
		}
		file.Data = data
		snapshot[gitPath] = file
	}
	return snapshot, nil
}

func parseGitDiffNameStatus(raw []byte) ([]string, []string, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	tokens := bytes.Split(raw, []byte{0})
	deletedSet := map[string]struct{}{}
	upsertSet := map[string]struct{}{}
	for i := 0; i < len(tokens); {
		if len(tokens[i]) == 0 {
			i++
			continue
		}
		statusValue := string(tokens[i])
		i++
		statusKind := statusValue[0]
		switch statusKind {
		case 'A', 'C', 'M', 'T':
			if i >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git diff status %q", statusValue)
			}
			pathText := string(tokens[i])
			i++
			upsertSet[pathText] = struct{}{}
		case 'D':
			if i >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git diff status %q", statusValue)
			}
			pathText := string(tokens[i])
			i++
			deletedSet[pathText] = struct{}{}
		case 'R':
			if i+1 >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git rename status %q", statusValue)
			}
			oldPath := string(tokens[i])
			newPath := string(tokens[i+1])
			i += 2
			deletedSet[oldPath] = struct{}{}
			upsertSet[newPath] = struct{}{}
		default:
			return nil, nil, fmt.Errorf("unsupported git diff status %q", statusValue)
		}
	}
	deleted := make([]string, 0, len(deletedSet))
	for p := range deletedSet {
		if _, upserted := upsertSet[p]; upserted {
			continue
		}
		deleted = append(deleted, p)
	}
	upserts := make([]string, 0, len(upsertSet))
	for p := range upsertSet {
		upserts = append(upserts, p)
	}
	sort.Strings(deleted)
	sort.Strings(upserts)
	return deleted, upserts, nil
}

func gitBlobContents(ctx context.Context, repoDir string, blobIDs []string) (map[string][]byte, error) {
	contents := make(map[string][]byte, len(blobIDs))
	if len(blobIDs) == 0 {
		return contents, nil
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = repoDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	writeErrCh := make(chan error, 1)
	go func() {
		w := bufio.NewWriter(stdin)
		for _, blobID := range blobIDs {
			if _, err := fmt.Fprintln(w, blobID); err != nil {
				_ = stdin.Close()
				writeErrCh <- err
				return
			}
		}
		if err := w.Flush(); err != nil {
			_ = stdin.Close()
			writeErrCh <- err
			return
		}
		writeErrCh <- stdin.Close()
	}()
	reader := bufio.NewReader(stdout)
	for range blobIDs {
		header, err := reader.ReadString('\n')
		if err != nil {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, err
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("malformed git cat-file header %q", strings.TrimSpace(header))
		}
		if fields[1] != "blob" {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("git object %s is %s, want blob", fields[0], fields[1])
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("invalid git blob size %q", fields[2])
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, err
		}
		trailing, err := reader.ReadByte()
		if err != nil {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, err
		}
		if trailing != '\n' {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("malformed git cat-file payload for %s", fields[0])
		}
		contents[fields[0]] = data
	}
	if err := <-writeErrCh; err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git cat-file --batch: %s", msg)
	}
	return contents, nil
}

func gitOutputBytes(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	out, err := gitOutputInput(ctx, dir, env, nil, args...)
	return []byte(out), err
}

func gitOutputInput(ctx context.Context, dir string, env []string, input []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), env...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

func gitObjectExists(ctx context.Context, dir, rev string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "-e", rev)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git cat-file -e failed: %w\n%s", err, string(out))
}

func gitMergeBaseIsAncestor(ctx context.Context, dir, base, head string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", base, head)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor failed: %w\n%s", err, string(out))
}

func writeReceivePackResult(w http.ResponseWriter, caps map[string]struct{}, result receivePackResult) {
	if result.UnpackStatus == "" {
		result.UnpackStatus = "ok"
	}
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	var out bytes.Buffer
	if hasCapability(caps, "side-band-64k") || hasCapability(caps, "side-band") {
		for _, msg := range result.Messages {
			appendSidebandPktLine(&out, 2, []byte(msg))
		}
		// Channel 1 carries the report-status pkt-line stream itself, so the
		// inner payload must be pkt-line encoded before sideband framing.
		var report bytes.Buffer
		appendPktLine(&report, []byte("unpack "+result.UnpackStatus+"\n"))
		for _, ref := range result.RefStatuses {
			appendPktLine(&report, []byte(formatRefStatus(ref)))
		}
		appendFlush(&report)
		appendSidebandPktLine(&out, 1, report.Bytes())
		appendFlush(&out)
		_, _ = w.Write(out.Bytes())
		return
	}
	appendPktLine(&out, []byte("unpack "+result.UnpackStatus+"\n"))
	for _, ref := range result.RefStatuses {
		appendPktLine(&out, []byte(formatRefStatus(ref)))
	}
	appendFlush(&out)
	_, _ = w.Write(out.Bytes())
}

func formatRefStatus(ref receivePackRefStatus) string {
	if ref.OK {
		return "ok " + ref.Ref + "\n"
	}
	msg := strings.TrimSpace(ref.Message)
	if msg == "" {
		msg = "rejected"
	}
	return "ng " + ref.Ref + " " + msg + "\n"
}

func appendPktLine(out *bytes.Buffer, payload []byte) {
	fmt.Fprintf(out, "%04x", len(payload)+4)
	out.Write(payload)
}

func appendSidebandPktLine(out *bytes.Buffer, channel byte, payload []byte) {
	packet := make([]byte, 0, len(payload)+1)
	packet = append(packet, channel)
	packet = append(packet, payload...)
	appendPktLine(out, packet)
}

func appendFlush(out *bytes.Buffer) {
	out.WriteString("0000")
}

func hasCapability(caps map[string]struct{}, cap string) bool {
	_, ok := caps[cap]
	return ok
}

func acceptedReceivePack(ref, message string) receivePackResult {
	return receivePackResult{
		UnpackStatus: "ok",
		RefStatuses:  []receivePackRefStatus{{Ref: ref, OK: true}},
		Messages:     []string{message},
	}
}

func rejectedReceivePack(ref, message string) receivePackResult {
	return receivePackResult{
		UnpackStatus: "ok",
		RefStatuses:  []receivePackRefStatus{{Ref: ref, Message: message}},
		Messages:     []string{message + "\n"},
	}
}

func userFacingError(err error) string {
	if err == nil {
		return ""
	}
	if st, ok := status.FromError(err); ok {
		return st.Message()
	}
	return strings.TrimSpace(err.Error())
}

func isZeroGitOID(oid string) bool {
	return oid == "" || oid == zeroGitOID
}

func shortGitOID(oid string) string {
	if len(oid) <= 12 {
		return oid
	}
	return oid[:12]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
