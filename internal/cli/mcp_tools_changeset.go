package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	grpcstatus "google.golang.org/grpc/status"

	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type mcpEdit struct {
	Op      string  `json:"op"`
	Path    string  `json:"path"`
	From    string  `json:"from"`
	Content *string `json:"content"`
}

func (b *mcpBackend) createChangeset(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Title       string    `json:"title"`
		Description string    `json:"description"`
		Slice       string    `json:"slice"`
		Submit      bool      `json:"submit"`
		Edits       []mcpEdit `json:"edits"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	if strings.TrimSpace(args.Title) == "" {
		return nil, userError("title_required", "a changeset needs a title", "Pass title, like a commit subject.")
	}
	if len(args.Edits) == 0 || len(args.Edits) > mcpMaxEditsPerCall {
		return nil, userError("invalid_edits", fmt.Sprintf("pass 1 to %d edits", mcpMaxEditsPerCall), "")
	}
	s, err := b.slice(ctx, args.Slice)
	if err != nil {
		return nil, err
	}
	commit, err := b.head(ctx)
	if err != nil {
		return nil, err
	}
	edits, err := b.fileEdits(ctx, s, args.Edits)
	if err != nil {
		return nil, err
	}
	client := corev1.NewChangesetServiceClient(b.conn)
	cs, err := client.CreateChangeset(b.ctx(ctx), &corev1.CreateChangesetRequest{
		AuthoringSlice: s.ref,
		TargetRef:      model.DefaultTargetRef,
		BaseCommitId:   commit,
		Title:          strings.TrimSpace(args.Title),
		Description:    args.Description,
	})
	if err != nil {
		return nil, err
	}
	patchset, err := client.UpdateChangeset(b.ctx(ctx), &corev1.UpdateChangesetRequest{ChangesetId: cs.GetId(), BaseCommitId: commit, FileEdits: edits})
	if err != nil {
		_, _ = client.AbandonChangeset(b.ctx(ctx), &corev1.AbandonChangesetRequest{ChangesetId: cs.GetId()})
		return nil, err
	}
	out := changesetHeader(cs)
	out["patchset"] = patchset.GetNumber()
	out["changed_paths"] = patchset.GetChangedPaths()
	out["status"] = "draft"
	if args.Submit {
		for key, value := range b.submit(ctx, cs.GetId(), patchset.GetId()) {
			out[key] = value
		}
	}
	return out, nil
}

func (b *mcpBackend) fileEdits(ctx context.Context, s mcpSlice, edits []mcpEdit) ([]*corev1.FileEdit, error) {
	out := make([]*corev1.FileEdit, 0, len(edits))
	writeDataByHash := map[string][]byte{}
	for i, edit := range edits {
		p, err := s.repoPath(edit.Path)
		if err != nil {
			return nil, err
		}
		switch edit.Op {
		case "write":
			if edit.Content == nil {
				return nil, userError("content_required", fmt.Sprintf("edit %d writes %s without content", i+1, p), "Pass the whole new file as content.")
			}
			data := []byte(*edit.Content)
			hash := objectid.RawContentHash(data)
			if _, seen := writeDataByHash[hash]; !seen {
				writeDataByHash[hash] = data
				if b.cache != nil {
					_, _ = b.cache.PutBytes(data)
				}
			}
			out = append(out, &corev1.FileEdit{Op: "upsert", Path: p, ContentHash: hash, Mode: 0o100644})
		case "delete", "mkdir":
			out = append(out, &corev1.FileEdit{Op: edit.Op, Path: p})
		case "move", "copy":
			from, err := s.repoPath(edit.From)
			if err != nil || strings.TrimSpace(edit.From) == "" {
				return nil, userError("from_required", fmt.Sprintf("edit %d (%s) needs from, the source path", i+1, edit.Op), "")
			}
			op := "copy"
			if edit.Op == "move" {
				op = "rename"
			}
			out = append(out, &corev1.FileEdit{Op: op, OldPath: from, Path: p})
		default:
			return nil, userError("invalid_op", fmt.Sprintf("edit %d has op %q", i+1, edit.Op), "Use write, delete, move, copy or mkdir.")
		}
	}
	if len(writeDataByHash) == 0 {
		return out, nil
	}

	tm := NewTransferManager(nil, corev1.NewBlobServiceClient(b.conn), s.ref, b.cache)
	if _, err := tm.AttachAndUploadBlobs(b.ctx(ctx), out, BlobBytesSources(writeDataByHash), nil, defaultUploadConcurrency()); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *mcpBackend) getChangeset(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Changeset string `json:"changeset"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	cs, err := corev1.NewChangesetServiceClient(b.conn).GetChangeset(b.ctx(ctx), &corev1.GetChangesetRequest{ChangesetId: strings.TrimSpace(args.Changeset)})
	if err != nil {
		return nil, err
	}
	out := changesetHeader(cs)
	out["status"] = cs.GetStatus()
	if reason := cs.GetSubmitBlockedReason(); reason != "" && cs.GetStatus() != "submitted" {
		out["blocked_reason"] = reason
	}
	if cs.GetCommitId() != "" {
		out["commit_id"] = cs.GetCommitId()
	}
	if current := currentPatchset(cs); current != nil {
		out["patchset"] = current.GetNumber()
		if current.GetKind() != "" {
			out["patchset_kind"] = current.GetKind()
		}
		out["changed_paths"] = current.GetChangedPaths()
		var conflicts []string
		for _, conflict := range current.GetConflicts() {
			conflicts = append(conflicts, conflict.GetPath())
		}
		if len(conflicts) > 0 {
			out["conflicts"] = conflicts
		}
		runs, err := corev1.NewCheckServiceClient(b.conn).ListCheckRuns(b.ctx(ctx), &corev1.ListCheckRunsRequest{ChangesetId: cs.GetId(), PatchsetId: current.GetId()})
		if err == nil {
			checks := []map[string]any{}
			for _, run := range LatestPatchsetCheckRuns(runs.GetRuns(), current.GetId()) {
				check := map[string]any{"name": run.GetCheckName(), "status": run.GetStatus()}
				if run.GetSummary() != "" {
					check["summary"] = run.GetSummary()
				}
				checks = append(checks, check)
			}
			out["checks"] = checks
		}
	}
	return out, nil
}

func (b *mcpBackend) submitChangeset(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Changeset string `json:"changeset"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	cs, err := corev1.NewChangesetServiceClient(b.conn).GetChangeset(b.ctx(ctx), &corev1.GetChangesetRequest{ChangesetId: strings.TrimSpace(args.Changeset)})
	if err != nil {
		return nil, err
	}
	out := changesetHeader(cs)
	for key, value := range b.submit(ctx, cs.GetId(), cs.GetCurrentPatchsetId()) {
		out[key] = value
	}
	return out, nil
}

// submit submits and says what became of it. A refusal is an answer, not an
// error: the changeset exists and its blocked_reason says what to do.
func (b *mcpBackend) submit(ctx context.Context, changesetID, patchsetID string) map[string]any {
	res, err := corev1.NewChangesetServiceClient(b.conn).SubmitChangeset(b.ctx(ctx), &corev1.SubmitChangesetRequest{ChangesetId: changesetID, ExpectedCurrentPatchsetId: patchsetID})
	if err != nil {
		out := map[string]any{"status": "blocked", "blocked_reason": grpcstatus.Convert(err).Message()}
		if check := pendingCheckPattern.FindStringSubmatch(grpcstatus.Convert(err).Message()); check != nil {
			out["waiting_for_check"] = check[1]
		}
		return out
	}
	out := map[string]any{"status": firstNonEmpty(res.GetStatus(), "submitted")}
	if res.GetCommitId() != "" {
		out["commit_id"] = res.GetCommitId()
	}
	return out
}

func changesetHeader(cs *corev1.Changeset) map[string]any {
	handle := firstNonEmpty(model.ShortChangesetID(cs.GetId()), cs.GetId())
	out := map[string]any{"changeset": handle, "changeset_id": cs.GetId(), "title": cs.GetTitle()}
	if cs.GetDescription() != "" {
		out["description"] = cs.GetDescription()
	}
	if url := webResourceURL("/cs/" + handle); url != "" {
		out["url"] = url
	}
	return out
}
