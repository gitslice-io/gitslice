package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	grpcstatus "google.golang.org/grpc/status"

	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

var pendingCheckPattern = regexp.MustCompile(`required check "([^"]+)" has no result`)

type EditSpec struct {
	Op      string  `json:"op"`
	Path    string  `json:"path"`
	From    string  `json:"from"`
	Content *string `json:"content"`
}

func (b *Backend) createChangeset(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Title       string     `json:"title"`
		Description string     `json:"description"`
		Slice       string     `json:"slice"`
		Submit      bool       `json:"submit"`
		Edits       []EditSpec `json:"edits"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	if strings.TrimSpace(args.Title) == "" {
		return nil, newUserError("title_required", "a changeset needs a title", "Pass title, like a commit subject.")
	}
	if len(args.Edits) == 0 || len(args.Edits) > mcpMaxEditsPerCall {
		return nil, newUserError("invalid_edits", fmt.Sprintf("pass 1 to %d edits", mcpMaxEditsPerCall), "")
	}
	s, err := b.slice(ctx, args.Slice)
	if err != nil {
		return nil, err
	}
	commit, err := b.head(ctx)
	if err != nil {
		return nil, err
	}
	edits, err := b.FileEdits(ctx, s, args.Edits)
	if err != nil {
		return nil, err
	}
	client := corev1.NewChangesetServiceClient(b.deps.Conn)
	cs, err := client.CreateChangeset(b.ctx(ctx), &corev1.CreateChangesetRequest{
		AuthoringSlice: s.Ref,
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
	out := b.changesetHeader(cs)
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

func (b *Backend) FileEdits(ctx context.Context, s Slice, edits []EditSpec) ([]*corev1.FileEdit, error) {
	out := make([]*corev1.FileEdit, 0, len(edits))
	writeDataByHash := map[string][]byte{}
	for i, edit := range edits {
		p, err := s.RepoPath(edit.Path)
		if err != nil {
			return nil, err
		}
		switch edit.Op {
		case "write":
			if edit.Content == nil {
				return nil, newUserError("content_required", fmt.Sprintf("edit %d writes %s without content", i+1, p), "Pass the whole new file as content.")
			}
			data := []byte(*edit.Content)
			hash := objectid.RawContentHash(data)
			if _, seen := writeDataByHash[hash]; !seen {
				writeDataByHash[hash] = data
				if b.deps.Cache != nil {
					_, _ = b.deps.Cache.PutBytes(data)
				}
			}
			out = append(out, &corev1.FileEdit{Op: "upsert", Path: p, ContentHash: hash, Mode: 0o100644})
		case "delete", "mkdir":
			out = append(out, &corev1.FileEdit{Op: edit.Op, Path: p})
		case "move", "copy":
			from, err := s.RepoPath(edit.From)
			if err != nil || strings.TrimSpace(edit.From) == "" {
				return nil, newUserError("from_required", fmt.Sprintf("edit %d (%s) needs from, the source path", i+1, edit.Op), "")
			}
			op := "copy"
			if edit.Op == "move" {
				op = "rename"
			}
			out = append(out, &corev1.FileEdit{Op: op, OldPath: from, Path: p})
		default:
			return nil, newUserError("invalid_op", fmt.Sprintf("edit %d has op %q", i+1, edit.Op), "Use write, delete, move, copy or mkdir.")
		}
	}
	if len(writeDataByHash) == 0 {
		return out, nil
	}
	if err := b.deps.UploadEdits(b.ctx(ctx), s.Ref, out, writeDataByHash); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *Backend) getChangeset(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Changeset string `json:"changeset"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	cs, err := corev1.NewChangesetServiceClient(b.deps.Conn).GetChangeset(b.ctx(ctx), &corev1.GetChangesetRequest{ChangesetId: strings.TrimSpace(args.Changeset)})
	if err != nil {
		return nil, err
	}
	out := b.changesetHeader(cs)
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
		runs, err := corev1.NewCheckServiceClient(b.deps.Conn).ListCheckRuns(b.ctx(ctx), &corev1.ListCheckRunsRequest{ChangesetId: cs.GetId(), PatchsetId: current.GetId()})
		if err == nil {
			dedup := b.deps.LatestCheckRuns
			if dedup == nil {
				dedup = LatestPatchsetCheckRuns
			}
			checks := []map[string]any{}
			for _, run := range dedup(runs.GetRuns(), current.GetId()) {
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

// LatestPatchsetCheckRuns returns the most recent attempt for each check on the
// given patchset (or across all patchsets if patchsetID is empty), sorted by check name.
func LatestPatchsetCheckRuns(runs []*corev1.CheckRun, patchsetID string) []*corev1.CheckRun {
	latest := map[string]*corev1.CheckRun{}
	for _, run := range runs {
		if run == nil {
			continue
		}
		if patchsetID != "" && run.GetPatchsetId() != patchsetID {
			continue
		}
		if prev := latest[run.GetCheckName()]; prev == nil || run.GetAttempt() > prev.GetAttempt() || (run.GetAttempt() == prev.GetAttempt() && run.GetCreatedAt() > prev.GetCreatedAt()) {
			latest[run.GetCheckName()] = run
		}
	}
	out := make([]*corev1.CheckRun, 0, len(latest))
	for _, run := range latest {
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetCheckName() < out[j].GetCheckName() })
	return out
}

func currentPatchset(cs *corev1.Changeset) *corev1.Patchset {
	if cs == nil || len(cs.Patchsets) == 0 {
		return nil
	}
	for _, patchset := range cs.Patchsets {
		if patchset.Id == cs.CurrentPatchsetId {
			return patchset
		}
	}
	return cs.Patchsets[len(cs.Patchsets)-1]
}

func (b *Backend) submitChangeset(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Changeset string `json:"changeset"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	cs, err := corev1.NewChangesetServiceClient(b.deps.Conn).GetChangeset(b.ctx(ctx), &corev1.GetChangesetRequest{ChangesetId: strings.TrimSpace(args.Changeset)})
	if err != nil {
		return nil, err
	}
	out := b.changesetHeader(cs)
	for key, value := range b.submit(ctx, cs.GetId(), cs.GetCurrentPatchsetId()) {
		out[key] = value
	}
	return out, nil
}

func (b *Backend) submit(ctx context.Context, changesetID, patchsetID string) map[string]any {
	res, err := corev1.NewChangesetServiceClient(b.deps.Conn).SubmitChangeset(b.ctx(ctx), &corev1.SubmitChangesetRequest{ChangesetId: changesetID, ExpectedCurrentPatchsetId: patchsetID})
	if err != nil {
		out := map[string]any{"status": "blocked", "blocked_reason": grpcstatus.Convert(err).Message()}
		if check := pendingCheckPattern.FindStringSubmatch(grpcstatus.Convert(err).Message()); check != nil {
			out["waiting_for_check"] = check[1]
		}
		return out
	}
	status := res.GetStatus()
	if status == "" {
		status = "submitted"
	}
	out := map[string]any{"status": status}
	if res.GetCommitId() != "" {
		out["commit_id"] = res.GetCommitId()
	}
	return out
}

func (b *Backend) changesetHeader(cs *corev1.Changeset) map[string]any {
	handle := model.ShortChangesetID(cs.GetId())
	if handle == "" {
		handle = cs.GetId()
	}
	out := map[string]any{"changeset": handle, "changeset_id": cs.GetId(), "title": cs.GetTitle()}
	if cs.GetDescription() != "" {
		out["description"] = cs.GetDescription()
	}
	if b.deps.WebResourceURL != nil {
		if url := b.deps.WebResourceURL("/cs/" + handle); url != "" {
			out["url"] = url
		}
	}
	return out
}
