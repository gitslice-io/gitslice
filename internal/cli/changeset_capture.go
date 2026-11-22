package cli

import (
	"context"
	"fmt"
	"time"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type captureDiagnostics struct {
	Result         string
	ChangedPaths   int
	Snapshot       snapshotEditsStats
	ChecksDuration time.Duration
	UpdateDuration time.Duration
	TotalDuration  time.Duration
}

// runChangesetCapture snapshots the current workspace edits and records them as a
// patchset, creating the changeset on first use and adding a patchset thereafter.
// It is a no-op when the workspace has no pending edits, so an agent daemon can
// call it after every turn without producing empty patchsets. The conversation
// link comes from the workspace config (WorkspaceConfig.ConversationID).
func (r Runner) runChangesetCapture(ctx context.Context, opts commandOptions, title string, noChecks bool) error {
	res, err := r.captureChangeset(ctx, opts, title, noChecks)
	if err != nil {
		return err
	}
	switch res.Outcome {
	case captureNoChanges:
		if !opts.jsonOutput() && !opts.Quiet {
			fmt.Fprintln(r.Stdout, "no changes to capture")
		}
	case captureUnchangedPatchset:
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, changesetOutput{ChangesetID: res.ChangesetID, PatchsetID: res.PatchsetID, PatchsetNumber: res.PatchsetNumber})
		}
		if !opts.Quiet {
			fmt.Fprintln(r.Stdout, "no changes to capture")
		}
	default:
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, changesetOutput{ChangesetID: res.ChangesetID, PatchsetID: res.PatchsetID, PatchsetNumber: res.PatchsetNumber})
		}
		if !opts.Quiet {
			fmt.Fprintln(r.Stdout, res.Summary())
		}
	}
	return nil
}

// What a capture did.
const (
	captureNoChanges         = "no_changes"
	captureUnchangedPatchset = "unchanged_patchset"
	captureCaptured          = "captured"
)

// captureResult is what captureChangeset did, as data: callers such as the
// agent daemon read it instead of parsing what gs cs capture prints.
type captureResult struct {
	Outcome        string
	ChangesetID    string
	PatchsetID     string
	PatchsetNumber int64
}

// Summary is the one line gs cs capture prints for a new patchset.
func (c captureResult) Summary() string {
	label := firstNonEmpty(model.ShortChangesetID(c.ChangesetID), c.ChangesetID)
	return fmt.Sprintf("captured changeset %s patchset %d", label, c.PatchsetNumber)
}

// captureChangeset snapshots the workspace's edits and records them as a
// patchset (see runChangesetCapture), reporting the outcome instead of
// printing it. Verbose capture diagnostics still go to the Runner's stderr.
func (r Runner) captureChangeset(ctx context.Context, opts commandOptions, title string, noChecks bool) (result captureResult, retErr error) {
	started := time.Now()
	diagnostics := captureDiagnostics{Result: "captured"}
	if opts.Verbose || opts.Debug || opts.Trace {
		defer func() {
			diagnostics.TotalDuration = time.Since(started)
			if retErr != nil {
				diagnostics.Result = "error"
			}
			r.writeCaptureDiagnostics(diagnostics)
		}()
	}

	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return result, err
	}
	conn, err := r.dialServer(ctx, cfg.ServerAddr)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	changesetClient := corev1.NewChangesetServiceClient(conn)

	edits, _, snapshotStats, err := r.snapshotEditsWithStats(ctx, conn, cfg, ws, true, defaultCaptureHashConcurrency(), defaultUploadConcurrency())
	diagnostics.Snapshot = snapshotStats
	diagnostics.ChangedPaths = len(edits)
	if err != nil {
		return result, err
	}
	if len(edits) == 0 {
		diagnostics.Result = "no_changes"
		return captureResult{Outcome: captureNoChanges}, nil
	}

	// Reuse the current draft changeset when one exists; otherwise create one.
	// priorPatchsetID is the changeset's current patchset as the server sees it
	// before this update; the server dedups no-op patchsets by returning that
	// same patchset, so we use it (not local state, which the agent daemon's
	// per-turn workspace doesn't reliably carry forward) to detect "no changes".
	reuse := false
	priorPatchsetID := ""
	if state.CurrentChangesetID != "" {
		cs, err := changesetClient.GetChangeset(callCtx, &corev1.GetChangesetRequest{ChangesetId: state.CurrentChangesetID})
		if err == nil {
			switch cs.Status {
			case "submitted", "abandoned":
			default:
				reuse = true
				priorPatchsetID = cs.CurrentPatchsetId
			}
		} else if grpcstatus.Code(err) != codes.NotFound {
			return result, err
		}
	}

	changesetID := state.CurrentChangesetID
	expectedPatchsetID := priorPatchsetID
	if !reuse {
		cs, err := changesetClient.CreateChangeset(callCtx, &corev1.CreateChangesetRequest{
			AuthoringSlice: &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
			TargetRef:      model.DefaultTargetRef,
			BaseCommitId:   state.BaseCommitID,
			Title:          title,
		})
		if err != nil {
			return result, err
		}
		changesetID = cs.Id
		expectedPatchsetID = ""
		priorPatchsetID = ""
	}

	var bundledCheckRuns []*corev1.BundledCheckRun
	if !noChecks {
		checksStarted := time.Now()
		bundledCheckRuns, err = r.captureBundledCheckRuns(ctx, opts, ws, changedPathsFromEdits(edits))
		diagnostics.ChecksDuration = time.Since(checksStarted)
		if err != nil {
			return result, err
		}
	}

	updateStarted := time.Now()
	patchset, err := changesetClient.UpdateChangeset(callCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: expectedPatchsetID,
		BaseCommitId:              state.BaseCommitID,
		FileEdits:                 edits,
		ConversationId:            ws.ConversationID,
		BundledCheckRuns:          bundledCheckRuns,
	})
	diagnostics.UpdateDuration = time.Since(updateStarted)
	if err != nil {
		return result, err
	}
	if priorPatchsetID != "" && patchset.Id == priorPatchsetID {
		diagnostics.Result = "unchanged_patchset"
		// No new patchset: the edits leave the changeset content unchanged.
		// Keep local state pointing at the live patchset so later turns agree.
		state.CurrentChangesetID = changesetID
		state.CurrentPatchsetID = patchset.Id
		if err := r.writeWorkspaceState(state); err != nil {
			return result, err
		}
		return captureResult{Outcome: captureUnchangedPatchset, ChangesetID: changesetID, PatchsetID: patchset.Id, PatchsetNumber: patchset.Number}, nil
	}
	state.CurrentChangesetID = changesetID
	state.CurrentPatchsetID = patchset.Id
	if err := r.writeWorkspaceState(state); err != nil {
		return result, err
	}
	return captureResult{Outcome: captureCaptured, ChangesetID: changesetID, PatchsetID: patchset.Id, PatchsetNumber: patchset.Number}, nil
}

func (r Runner) writeCaptureDiagnostics(stats captureDiagnostics) {
	formatDuration := func(value time.Duration) time.Duration {
		return value.Round(time.Millisecond)
	}
	fmt.Fprintf(
		r.stderr(),
		"capture diagnostics: result=%s files=%d hashed_bytes=%d changed_paths=%d unique_blobs=%d remote_hits=%d uploaded_blobs=%d uploaded_bytes=%d hash_workers=%d upload_workers=%d walk=%s hash=%s diff=%s blob_status=%s upload=%s checks=%s update=%s total=%s\n",
		stats.Result,
		stats.Snapshot.Scan.FileCount,
		stats.Snapshot.Scan.ByteCount,
		stats.ChangedPaths,
		stats.Snapshot.Blobs.RequiredBlobs,
		stats.Snapshot.Blobs.AvailableBlobs,
		stats.Snapshot.Blobs.UploadedBlobs,
		stats.Snapshot.Blobs.UploadedBytes,
		stats.Snapshot.Scan.HashWorkers,
		stats.Snapshot.Blobs.UploadWorkers,
		formatDuration(stats.Snapshot.Scan.WalkDuration),
		formatDuration(stats.Snapshot.Scan.HashDuration),
		formatDuration(stats.Snapshot.DiffDuration),
		formatDuration(stats.Snapshot.Blobs.StatusDuration),
		formatDuration(stats.Snapshot.Blobs.UploadDuration),
		formatDuration(stats.ChecksDuration),
		formatDuration(stats.UpdateDuration),
		formatDuration(stats.TotalDuration),
	)
}
