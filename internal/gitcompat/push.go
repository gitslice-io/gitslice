package gitcompat

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
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
	if ok, err := gitObjectExists(ctx, pushRepo, cmd.NewOID+"^{commit}"); err != nil {
		return rejectedReceivePack(cmd.Ref, userFacingError(err))
	} else if !ok {
		return rejectedReceivePack(cmd.Ref, fmt.Sprintf("pushed object %s is not a commit", shortGitOID(cmd.NewOID)))
	}
	base, err := resolvePushBase(ctx, pushRepo, projection, cmd.NewOID)
	if err != nil {
		return rejectedReceivePack(cmd.Ref, userFacingError(err))
	}
	// A push on top of the projected head is based on the current native
	// head. A push on an older projected commit keeps that commit's native
	// base, so changeset validation sees what changed since and reports
	// conflicts instead of silently overwriting them.
	baseNative := projection.NativeCommitID
	if base != "" && base != projection.GitCommitID {
		baseNative, _ = projection.NativeCommitFor(base)
	}
	edits, title, err := pushedCommitFileEdits(ctx, serviceCtx, pushRepo, base, cmd.NewOID, sliceRef, h.blobs)
	if err != nil {
		return rejectedReceivePack(cmd.Ref, userFacingError(err))
	}
	if len(edits) == 0 {
		return rejectedReceivePack(cmd.Ref, "push does not change any files relative to the projected commit it is based on")
	}
	if err := h.ensurePushEditsContained(ctx, sliceRef, edits); err != nil {
		return rejectedReceivePack(cmd.Ref, userFacingError(err))
	}

	if target.New {
		cs, err := h.changesets.CreateChangeset(serviceCtx, &corev1.CreateChangesetRequest{
			AuthoringSlice: sliceRef,
			TargetRef:      storage.DefaultTargetRef,
			BaseCommitId:   baseNative,
			Title:          title,
			Description:    "Created from git push to refs/changes/new.",
		})
		if err != nil {
			return rejectedReceivePack(cmd.Ref, userFacingError(err))
		}
		patchset, err := h.changesets.UpdateChangeset(serviceCtx, &corev1.UpdateChangesetRequest{
			ChangesetId:  cs.Id,
			BaseCommitId: baseNative,
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
		BaseCommitId:              baseNative,
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
