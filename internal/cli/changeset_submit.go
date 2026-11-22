package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/lazyfs"
	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	grpcstatus "google.golang.org/grpc/status"
)

type submitDependenciesOutput struct {
	DependencyID string                         `json:"dependency_id"`
	Status       string                         `json:"status"`
	Results      []submitDependencyResultOutput `json:"results"`
}

type submitDependencyResultOutput struct {
	ChangesetID      string `json:"changeset_id"`
	Status           string `json:"status"`
	CommitID         string `json:"commit_id,omitempty"`
	PendingPublishID string `json:"pending_publish_id,omitempty"`
	BlockedReason    string `json:"blocked_reason,omitempty"`
}

// submitWhenChecksReport submits a changeset gs create just made. Its required
// checks start when it is created, so a submit right away is often blocked
// only because they have not reported; it waits for them (up to timeout) and
// tries again. Anything else that blocks the submit is returned as it is.
func (r Runner) submitWhenChecksReport(ctx context.Context, opts commandOptions, conn *grpc.ClientConn, cfg UserConfig, state WorkspaceState, timeout time.Duration) (*corev1.SubmitStackResponse, error) {
	var res *corev1.SubmitStackResponse
	err := r.retrySubmitWhileChecksPending(ctx, opts, conn, cfg, timeout, func(remaining time.Duration) (string, string, error) {
		var err error
		res, err = r.submitActivePath(ctx, conn, cfg, state, true, remaining)
		if err != nil {
			return "", "", err
		}
		for _, result := range res.Results {
			if check := pendingRequiredCheckName(result.BlockedReason); check != "" {
				return check, result.ChangesetId, nil
			}
		}
		return "", "", nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (r Runner) runChangesetSubmit(ctx context.Context, opts commandOptions, requestedID string, watch bool, watchTimeout time.Duration) error {
	target, err := r.resolveChangesetTarget(requestedID)
	if err != nil {
		return err
	}
	cfg, ws, state, hasWorkspace, changesetID, usingWorkspaceCurrent := target.Config, target.Workspace, target.State, target.HasWorkspace, target.ChangesetID, target.UsingWorkspaceCurrent
	conn, err := r.dialServer(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	changesetClient := corev1.NewChangesetServiceClient(conn)
	cs, err := changesetClient.GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
	if err != nil {
		return err
	}
	changesetLabel := firstNonEmpty(displayChangesetID(cs), changesetID)
	expectedPatchsetID := cs.CurrentPatchsetId
	if usingWorkspaceCurrent && state.CurrentPatchsetID != "" {
		expectedPatchsetID = state.CurrentPatchsetID
	}
	if usingWorkspaceCurrent && hasWorkspace {
		conflictState, hasConflicts, err := r.readWorkspaceConflictState()
		if err != nil {
			return err
		}
		if hasConflicts && conflictState.ChangesetID == changesetID && (conflictState.PatchsetID == "" || conflictState.PatchsetID == expectedPatchsetID) {
			return userError("unresolved_conflicts", "workspace has unresolved sync conflicts", "Resolve conflicts and run gs cs update before submitting.")
		}
	}
	submitReq := &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: expectedPatchsetID,
	}
	var res *corev1.SubmitChangesetResponse
	if watch {
		// Required checks that have not reported yet are waited for (up to the
		// watch timeout), as gs create --submit does.
		var submitErr error
		if err := r.retrySubmitWhileChecksPending(ctx, opts, conn, cfg, watchTimeout, func(_ time.Duration) (string, string, error) {
			res, submitErr = changesetClient.SubmitChangeset(authContext(ctx, cfg), submitReq)
			if submitErr != nil {
				if check := pendingRequiredCheckName(grpcstatus.Convert(submitErr).Message()); check != "" {
					return check, changesetID, nil
				}
				return "", "", submitErr
			}
			return "", "", nil
		}); err != nil {
			return err
		}
		err = submitErr
	} else {
		res, err = changesetClient.SubmitChangeset(authContext(ctx, cfg), submitReq)
	}
	if err != nil {
		return err
	}
	commitID := res.CommitId
	refCommitID := res.NewRefCommitId
	status := res.Status
	if watch && (refCommitID == "" || res.Status == "pending_publish") {
		var err error
		commitID, refCommitID, err = r.waitForChangesetPublished(ctx, conn, cfg, changesetID, changesetLabel, watchTimeout, !opts.Quiet && !opts.JSONOutput())
		if err != nil {
			return err
		}
		status = "submitted"
	}
	if status == "" {
		status = "submitted"
	}
	// A lazy workspace moves its layers onto what landed instead of being
	// rescanned, which would fetch every file of the slice.
	var lazy *lazyWorkspace
	if hasWorkspace {
		if lazy, err = r.currentLazyWorkspace(); err != nil {
			return err
		}
	}
	// A server that merged the changeset onto a newer base (merge on submit)
	// landed more than this workspace holds: its files are not the new base,
	// so gs sync brings the merge in rather than this taking them as landed.
	mergedOnSubmit := false
	if status == "submitted" && usingWorkspaceCurrent && hasWorkspace {
		if landed, err := changesetClient.GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID}); err == nil {
			mergedOnSubmit = landedByServerMerge(landed, expectedPatchsetID)
		}
	}
	if mergedOnSubmit {
		if !opts.Quiet && !opts.JSONOutput() {
			fmt.Fprintln(r.stderr(), "the base had moved, so the server merged this changeset onto the latest base before landing it; run gs sync to bring the merge into this workspace")
		}
	} else if status == "submitted" && usingWorkspaceCurrent && lazy != nil {
		if err := r.landLazyWorkspace(ctx, conn, cfg, ws, lazy, landedPatchset(cs, expectedPatchsetID), refCommitID, state); err != nil {
			fmt.Fprintf(r.stderr(), "warning: the changeset landed, but the lazy workspace keeps its old base (%v); run gs sync\n", err)
		}
	} else if status == "submitted" && usingWorkspaceCurrent && hasWorkspace {
		current, err := r.scanWorkspaceFiles(ws)
		if err != nil {
			return err
		}
		if err := r.commitWorkspaceBase(ws, state, BaseSnapshot{CommitID: refCommitID, Files: snapshotFiles(current)}); err != nil {
			return err
		}
	}
	handle := firstNonEmpty(model.ShortChangesetID(changesetID), changesetID)
	webURL := webResourceURL("/cs/" + handle)
	if opts.JSONOutput() {
		// changeset and changeset_url name the changeset; commit_id is the
		// native commit it landed as, which is not a changeset id and has no
		// /cs/ page. Share changeset_url.
		return r.writeJSONOutput(opts, map[string]any{
			"changeset_id":      cs.Id,
			"changeset":         handle,
			"changeset_url":     webURL,
			"commit_id":         commitID,
			"target_ref":        res.TargetRef,
			"new_ref_commit_id": refCommitID,
			"status":            status,
			"web_url":           webURL,
		})
	}
	if opts.Quiet {
		return nil
	}
	if status != "submitted" {
		label := firstNonEmpty(changesetLabel, changesetID)
		fmt.Fprintf(r.Stdout, "submit accepted for %s; status: %s\n", label, status)
		fmt.Fprintf(r.Stdout, "Run gs cs status --watch %s to wait for publish.\n", label)
		if webURL != "" {
			fmt.Fprintf(r.Stdout, "view: %s\n", webURL)
		}
		return nil
	}
	// The changeset and its link come first: agents copy the first id they
	// see, and the commit id is not a changeset id (its /cs/ link is a 404).
	fmt.Fprintf(r.Stdout, "submitted changeset %s to %s\n", handle, res.TargetRef)
	if webURL != "" {
		fmt.Fprintf(r.Stdout, "view: %s\n", webURL)
	}
	if commitID != "" {
		fmt.Fprintf(r.Stdout, "landed as commit %s (a commit id, not a changeset id; share the view link)\n", commitID)
	}
	return nil
}

func (r Runner) runStackSubmit(ctx context.Context, opts commandOptions, requestedID string, stackFlag bool, subtree string, watch bool, watchTimeout time.Duration) error {
	if !stackFlag && strings.TrimSpace(subtree) == "" {
		if strings.TrimSpace(requestedID) == "" {
			cfg, _, state, err := r.loadLocalState()
			if err != nil {
				return err
			}
			if err := rejectPreStackWorkspaceState(state); err != nil {
				return err
			}
			if state.ActiveStackID != "" && state.CurrentChangesetID != "" {
				return r.runStackSubmitActivePath(ctx, opts, cfg, state, watch, watchTimeout)
			}
		}
		return r.runChangesetSubmit(ctx, opts, requestedID, watch, watchTimeout)
	}
	target, err := r.resolveChangesetTarget(requestedID)
	if err != nil {
		return err
	}
	cfg, state := target.Config, target.State
	if err := rejectPreStackWorkspaceState(state); err != nil {
		return err
	}
	stackID := state.ActiveStackID
	if stackID == "" {
		if requestedID != "" {
			cs, err := r.getChangeset(ctx, cfg, requestedID)
			if err != nil {
				return err
			}
			stackID = cs.StackId
		}
	}
	if stackID == "" {
		return userError("no_active_stack", "no active dependency tree in workspace", "Run gs create first or pass a dependent changeset id.")
	}
	conn, err := r.dialServer(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewChangesetStackServiceClient(conn).SubmitStack(authContext(ctx, cfg), &corev1.SubmitStackRequest{
		StackId:                stackID,
		SubtreeRootChangesetId: strings.TrimSpace(subtree),
	})
	if err != nil {
		return err
	}
	if watch {
		changesetClient := corev1.NewChangesetServiceClient(conn)
		for _, result := range res.Results {
			if result.Status == "pending_publish" || result.Status == "submitted" {
				_, _, _ = r.waitForChangesetPublished(ctx, conn, cfg, result.ChangesetId, result.ChangesetId, watchTimeout, false)
				_, _ = changesetClient.GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: result.ChangesetId})
			}
		}
	}
	if err := r.writeSubmitDependencies(opts, res); err != nil {
		return err
	}
	r.reportFailingChecks(ctx, conn, cfg, opts, res)
	return nil
}

func (r Runner) runStackSubmitActivePath(ctx context.Context, opts commandOptions, cfg UserConfig, state WorkspaceState, watch bool, watchTimeout time.Duration) error {
	conn, err := r.dialServer(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	var res *corev1.SubmitStackResponse
	if watch {
		// Required checks that have not reported yet are waited for.
		res, err = r.submitWhenChecksReport(ctx, opts, conn, cfg, state, watchTimeout)
	} else {
		res, err = r.submitActivePath(ctx, conn, cfg, state, false, watchTimeout)
	}
	if err != nil {
		return err
	}
	if err := r.writeSubmitDependencies(opts, res); err != nil {
		return err
	}
	r.reportFailingChecks(ctx, conn, cfg, opts, res)
	// Something the server would not submit is a failed submit, as with
	// gs create --submit: a script must not read it as landed.
	if res.Status == "partial" {
		return userError("submit_blocked", "could not submit every changeset", "Fix what blocks it (above), then run gs submit again.")
	}
	return nil
}

// submitActivePath submits the active changeset and the changesets it is
// based on, base first, and says what became of each.
func (r Runner) submitActivePath(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, state WorkspaceState, watch bool, watchTimeout time.Duration) (*corev1.SubmitStackResponse, error) {
	callCtx := authContext(ctx, cfg)
	stack, err := corev1.NewChangesetStackServiceClient(conn).GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
	if err != nil {
		return nil, err
	}
	entry := stackEntryByChangesetSelector(stack, state.CurrentChangesetID)
	if entry == nil {
		return nil, userError("stack_entry_not_found", "active changeset not found", "Run gs deps to inspect available changesets.")
	}
	entriesByID := map[string]*corev1.ChangesetStackEntry{}
	for _, item := range stack.Entries {
		if item != nil {
			entriesByID[item.ChangesetId] = item
		}
	}
	pathEntries := []*corev1.ChangesetStackEntry{}
	for entry != nil {
		pathEntries = append(pathEntries, entry)
		if entry.ParentChangesetId == "" {
			break
		}
		entry = entriesByID[entry.ParentChangesetId]
	}
	for i, j := 0, len(pathEntries)-1; i < j; i, j = i+1, j-1 {
		pathEntries[i], pathEntries[j] = pathEntries[j], pathEntries[i]
	}
	changesetClient := corev1.NewChangesetServiceClient(conn)
	res := &corev1.SubmitStackResponse{StackId: state.ActiveStackID, Status: "submitted"}
	blocked := false
	for _, item := range pathEntries {
		if item.State == "needs_restack" {
			blocked = true
			res.Results = append(res.Results, &corev1.SubmitStackEntryResult{
				ChangesetId:   item.ChangesetId,
				Status:        "blocked",
				BlockedReason: "NeedsBaseUpdate",
			})
			continue
		}
		cs := item.Changeset
		if cs == nil {
			cs, err = changesetClient.GetChangeset(callCtx, &corev1.GetChangesetRequest{ChangesetId: item.ChangesetId})
			if err != nil {
				return nil, err
			}
		}
		if cs.Status == "submitted" {
			res.Results = append(res.Results, &corev1.SubmitStackEntryResult{ChangesetId: cs.Id, Status: "submitted", CommitId: cs.CommitId})
			continue
		}
		submitted, err := changesetClient.SubmitChangeset(callCtx, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id, ExpectedCurrentPatchsetId: cs.CurrentPatchsetId})
		if err != nil {
			blocked = true
			res.Results = append(res.Results, &corev1.SubmitStackEntryResult{ChangesetId: cs.Id, Status: "blocked", BlockedReason: cliSubmitBlockReason(err)})
			continue
		}
		result := &corev1.SubmitStackEntryResult{
			ChangesetId:      cs.Id,
			Status:           submitted.Status,
			CommitId:         submitted.CommitId,
			PendingPublishId: submitted.PendingPublishId,
		}
		if watch && (submitted.Status == "pending_publish" || submitted.Status == "submitted") {
			// Once it has landed, say so rather than what the submit first said.
			if commitID, _, err := r.waitForChangesetPublished(ctx, conn, cfg, cs.Id, cs.Id, watchTimeout, false); err == nil && commitID != "" {
				result.Status = "submitted"
				result.CommitId = commitID
				result.PendingPublishId = ""
			}
		}
		res.Results = append(res.Results, result)
	}
	if blocked {
		res.Status = "partial"
	}
	return res, nil
}

func (r Runner) writeSubmitDependencies(opts commandOptions, res *corev1.SubmitStackResponse) error {
	displaySubmitStackResponse(res)
	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, submitDependenciesOutputFromProto(res))
	}
	if opts.Quiet {
		return nil
	}
	r.writeSubmitResultsText(res)
	return nil
}

func (r Runner) writeSubmitResultsText(res *corev1.SubmitStackResponse) {
	for _, result := range res.Results {
		if result.BlockedReason != "" {
			fmt.Fprintf(r.Stdout, "blocked %s: %s\n", firstNonEmpty(model.ShortChangesetID(result.ChangesetId), result.ChangesetId), result.BlockedReason)
			continue
		}
		fmt.Fprintf(r.Stdout, "%s %s\n", result.Status, firstNonEmpty(model.ShortChangesetID(result.ChangesetId), result.ChangesetId))
	}
	if res.Status == "partial" {
		fmt.Fprintln(r.Stdout, "dependencies partially submitted")
	}
}

func cliSubmitBlockReason(err error) string {
	if err == nil {
		return ""
	}
	if st, ok := grpcstatus.FromError(err); ok {
		if st.Message() != "" {
			return st.Message()
		}
		return st.Code().String()
	}
	return err.Error()
}

func displaySubmitStackResponse(res *corev1.SubmitStackResponse) {
	if res == nil {
		return
	}
	for _, result := range res.Results {
		if result != nil {
			result.BlockedReason = displaySubmitBlockedReason(result.BlockedReason)
		}
	}
}

func submitDependenciesOutputFromProto(res *corev1.SubmitStackResponse) submitDependenciesOutput {
	out := submitDependenciesOutput{}
	if res == nil {
		return out
	}
	out.DependencyID = res.StackId
	out.Status = res.Status
	out.Results = make([]submitDependencyResultOutput, 0, len(res.Results))
	for _, result := range res.Results {
		if result == nil {
			continue
		}
		out.Results = append(out.Results, submitDependencyResultOutput{
			ChangesetID:      result.ChangesetId,
			Status:           result.Status,
			CommitID:         result.CommitId,
			PendingPublishID: result.PendingPublishId,
			BlockedReason:    displaySubmitBlockedReason(result.BlockedReason),
		})
	}
	return out
}

func displaySubmitBlockedReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "NeedsRestack", "NeedsBaseUpdate":
		return "needs base update"
	case "BlockedOnStackParent", "BlockedOnBaseChangeset":
		return "base changeset has not landed"
	default:
		return reason
	}
}

// landedPatchset is the patchset a submit landed: the one it expected, else
// the changeset's current one.
// landedByServerMerge reports whether the changeset landed as a patchset the
// server made by merging submitted onto a newer base, rather than as
// submitted itself.
func landedByServerMerge(cs *corev1.Changeset, submitted string) bool {
	current := currentPatchset(cs)
	return current != nil && submitted != "" && current.GetId() != submitted && current.GetKind() == model.SubmitMergeKind
}

func landedPatchset(cs *corev1.Changeset, patchsetID string) *corev1.Patchset {
	for _, patchset := range cs.GetPatchsets() {
		if patchset.GetId() == patchsetID {
			return patchset
		}
	}
	return currentPatchset(cs)
}

// landLazyWorkspace moves a lazy workspace onto the commit its changeset
// landed in. Local changes the changeset carried leave the local layer: the
// new base has them, merged with whatever landed meanwhile. Changes made
// since the last gs create or gs modify stay local.
func (r Runner) landLazyWorkspace(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, lazy *lazyWorkspace, landed *corev1.Patchset, commitID string, state WorkspaceState) error {
	manifest, err := r.lazyListingAt(ctx, conn, cfg, lazy, commitID)
	if err != nil {
		return err
	}
	carried := map[string]string{}
	for _, edit := range landed.GetFileEdits() {
		switch edit.GetOp() {
		case "delete":
			carried[edit.GetPath()] = "delete"
		case "mkdir":
		default:
			if edit.GetOp() == "rename" {
				carried[edit.GetOldPath()] = "delete"
			}
			if edit.GetContentHash() != "" {
				carried[edit.GetPath()] = editFingerprint(lazyfs.Edit{Op: "upsert", ContentHash: edit.GetContentHash(), Mode: edit.GetMode()})
			}
		}
	}
	if _, err := r.rebaseLazyWorkspace(lazy, manifest, func(edit lazyfs.Edit) mountResolution {
		if carried[edit.Path] == editFingerprint(edit) {
			return mountResolution{Theirs: true}
		}
		return mountResolution{Keep: true}
	}); err != nil {
		return err
	}
	return r.commitWorkspaceBase(ws, state, manifestSnapshot(manifest))
}
