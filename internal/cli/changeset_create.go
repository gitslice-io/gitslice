package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/storage"
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

func (r Runner) runChangesetCreate(ctx context.Context, opts commandOptions, title string, empty emptyChangesetOptions, review reviewOptions) error {
	if err := empty.validate(); err != nil {
		return err
	}
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	changesetClient := corev1.NewChangesetServiceClient(conn)
	if err := r.guardChangesetCreate(ctx, cfg, state, changesetClient); err != nil {
		return err
	}
	base, err := r.readBaseSnapshot()
	if err != nil {
		return err
	}
	edits, _, err := r.snapshotEditsReviewed(ctx, conn, cfg, ws, base, r.reviewEdits(opts, ws, base, review))
	if err != nil {
		return err
	}
	if len(edits) == 0 && !empty.allowEmpty {
		return r.nothingToChange(opts, empty.ifChanged)
	}
	callCtx := authContext(ctx, cfg)
	cs, err := changesetClient.CreateChangeset(callCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   state.BaseCommitID,
		Title:          title,
	})
	if err != nil {
		return err
	}
	patchset, err := changesetClient.UpdateChangeset(callCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:    cs.Id,
		BaseCommitId:   state.BaseCommitID,
		FileEdits:      edits,
		ConversationId: ws.ConversationID,
	})
	if err != nil {
		return err
	}
	state.CurrentChangesetID = cs.Id
	state.CurrentPatchsetID = patchset.Id
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	label := firstNonEmpty(displayChangesetID(cs), cs.Id)
	webURL := webResourceURL("/cs/" + label)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, changesetOutput{
			ChangesetID:    cs.Id,
			PatchsetID:     patchset.Id,
			PatchsetNumber: patchset.Number,
			WebURL:         webURL,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "created changeset %s patchset %d\n", label, patchset.Number)
	if webURL != "" {
		fmt.Fprintf(r.Stdout, "view: %s\n", webURL)
	}
	return nil
}

// runChangesetCapture snapshots the current workspace edits and records them as a
// patchset, creating the changeset on first use and adding a patchset thereafter.
// It is a no-op when the workspace has no pending edits, so an agent daemon can
// call it after every turn without producing empty patchsets. The conversation
// link comes from the workspace config (WorkspaceConfig.ConversationID).
func (r Runner) runChangesetCapture(ctx context.Context, opts commandOptions, title string, noChecks bool) (retErr error) {
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
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	changesetClient := corev1.NewChangesetServiceClient(conn)

	edits, _, snapshotStats, err := r.snapshotEditsWithStats(ctx, conn, cfg, ws, true, defaultCaptureHashConcurrency(), defaultUploadConcurrency())
	diagnostics.Snapshot = snapshotStats
	diagnostics.ChangedPaths = len(edits)
	if err != nil {
		return err
	}
	if len(edits) == 0 {
		diagnostics.Result = "no_changes"
		if !opts.jsonOutput() && !opts.Quiet {
			fmt.Fprintln(r.Stdout, "no changes to capture")
		}
		return nil
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
			return err
		}
	}

	changesetID := state.CurrentChangesetID
	expectedPatchsetID := priorPatchsetID
	if !reuse {
		cs, err := changesetClient.CreateChangeset(callCtx, &corev1.CreateChangesetRequest{
			AuthoringSlice: &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
			TargetRef:      postgres.DefaultTargetRef,
			BaseCommitId:   state.BaseCommitID,
			Title:          title,
		})
		if err != nil {
			return err
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
			return err
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
		return err
	}
	if priorPatchsetID != "" && patchset.Id == priorPatchsetID {
		diagnostics.Result = "unchanged_patchset"
		// No new patchset: the edits leave the changeset content unchanged.
		// Keep local state pointing at the live patchset so later turns agree.
		state.CurrentChangesetID = changesetID
		state.CurrentPatchsetID = patchset.Id
		if err := r.writeWorkspaceState(state); err != nil {
			return err
		}
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, changesetOutput{
				ChangesetID:    changesetID,
				PatchsetID:     patchset.Id,
				PatchsetNumber: patchset.Number,
			})
		}
		if !opts.Quiet {
			fmt.Fprintln(r.Stdout, "no changes to capture")
		}
		return nil
	}
	state.CurrentChangesetID = changesetID
	state.CurrentPatchsetID = patchset.Id
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}

	label := firstNonEmpty(storage.ShortChangesetID(changesetID), changesetID)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, changesetOutput{
			ChangesetID:    changesetID,
			PatchsetID:     patchset.Id,
			PatchsetNumber: patchset.Number,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "captured changeset %s patchset %d\n", label, patchset.Number)
	return nil
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

// emptyChangesetOptions says what gs create does when the workspace has no
// edits: by default it refuses, so a scheduled job does not land empty
// commits.
type emptyChangesetOptions struct {
	// ifChanged makes "nothing changed" a success that creates nothing.
	ifChanged bool
	// allowEmpty creates the empty changeset anyway.
	allowEmpty bool
}

func (o *emptyChangesetOptions) addFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&o.ifChanged, "if-changed", false, "when nothing changed, create nothing and exit 0 (for scheduled jobs)")
	cmd.Flags().BoolVar(&o.allowEmpty, "allow-empty", false, "create the changeset even when nothing changed")
}

func (o emptyChangesetOptions) validate() error {
	if o.ifChanged && o.allowEmpty {
		return userError("invalid_args", "use --if-changed or --allow-empty, not both", "")
	}
	return nil
}

// nothingToChange answers a create with no edits: a no-op with --if-changed,
// else an error.
func (r Runner) nothingToChange(opts commandOptions, ifChanged bool) error {
	if !ifChanged {
		return userError("nothing_to_change", "nothing changed since the base, so no changeset was created",
			"Edit files first. In a scheduled job, pass --if-changed to make this a no-op that exits 0; --allow-empty creates an empty changeset anyway.")
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"created": false, "reason": "nothing_to_change"})
	}
	if !opts.Quiet {
		fmt.Fprintln(r.Stdout, "nothing changed; no changeset created")
	}
	return nil
}

type createSubmitOptions struct {
	enabled bool
	timeout time.Duration
}

func (r Runner) runStackCreate(ctx context.Context, opts commandOptions, title, description, parentSelector string, root, sibling, all bool, submit createSubmitOptions, empty emptyChangesetOptions, review reviewOptions) error {
	_ = all
	if err := empty.validate(); err != nil {
		return err
	}
	title, err := r.requiredCommandMessage(opts, title, "Changeset title: ", "create requires --message", "Run gs create --message <title>.")
	if err != nil {
		return err
	}
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if err := rejectPreStackWorkspaceState(state); err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	stackClient := corev1.NewChangesetStackServiceClient(conn)
	changesetClient := corev1.NewChangesetServiceClient(conn)

	stackID := state.ActiveStackID
	var stack *corev1.ChangesetStack
	newStack := root || stackID == ""
	if newStack {
		// The dependency tree is created once there are edits to put in it;
		// until then it has no entries, as a new one would.
		stack = &corev1.ChangesetStack{BaseCommitId: state.BaseCommitID}
	} else {
		stack, err = stackClient.GetStack(callCtx, &corev1.GetStackRequest{StackId: stackID})
		if err != nil {
			return err
		}
	}

	parentID := strings.TrimSpace(parentSelector)
	parentPatchsetID := ""
	if !root && parentID == "" && state.CurrentChangesetID != "" {
		parentID = state.CurrentChangesetID
	}
	if sibling && parentID != "" {
		parentEntry := stackEntryByChangesetSelector(stack, parentID)
		if parentEntry == nil {
			return userError("stack_entry_not_found", "active changeset not found", "Run gs deps to inspect available changesets.")
		}
		parentID = parentEntry.ParentChangesetId
	}
	if parentID != "" {
		parent, err := changesetClient.GetChangeset(callCtx, &corev1.GetChangesetRequest{ChangesetId: parentID})
		if err != nil {
			return err
		}
		parentID = parent.Id
		parentPatchsetID = parent.CurrentPatchsetId
		if parentPatchsetID == "" {
			return userError("parent_has_no_patchset", "base changeset has no current patchset", "Run gs modify on the parent before creating a child.")
		}
	}

	base, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, stack, parentID)
	if err != nil {
		return err
	}
	edits, _, err := r.snapshotEditsReviewed(ctx, conn, cfg, ws, base, r.reviewEdits(opts, ws, base, review))
	if err != nil {
		return err
	}
	if len(edits) == 0 && !empty.allowEmpty {
		return r.nothingToChange(opts, empty.ifChanged)
	}
	if newStack {
		stack, err = stackClient.CreateStack(callCtx, &corev1.CreateStackRequest{
			AuthoringSlice: &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
			TargetRef:      postgres.DefaultTargetRef,
			BaseCommitId:   state.BaseCommitID,
			Title:          title,
		})
		if err != nil {
			return err
		}
		stackID = stack.Id
	}
	cs, err := stackClient.AddStackEntry(callCtx, &corev1.AddStackEntryRequest{
		StackId:           stackID,
		Title:             title,
		Description:       description,
		ParentChangesetId: parentID,
		ParentPatchsetId:  parentPatchsetID,
	})
	if err != nil {
		return err
	}
	updateReq := &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: state.BaseCommitID,
		FileEdits:    edits,
	}
	if parentPatchsetID != "" {
		updateReq.BaseKind = "patchset"
		updateReq.BasePatchsetId = parentPatchsetID
		updateReq.ExpectedParentPatchsetId = parentPatchsetID
	}
	patchset, err := changesetClient.UpdateChangeset(callCtx, updateReq)
	if err != nil {
		return err
	}
	state.ActiveStackID = stackID
	state.CurrentChangesetID = cs.Id
	state.CurrentPatchsetID = patchset.Id
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	link := webResourceURL("/cs/" + firstNonEmpty(displayChangesetID(cs), cs.Id))
	if !opts.jsonOutput() && !opts.Quiet {
		fmt.Fprintf(r.Stdout, "created %s patchset %d\n", firstNonEmpty(displayChangesetID(cs), cs.Id), patchset.Number)
		if link != "" {
			fmt.Fprintf(r.Stdout, "view: %s\n", link)
		}
		fmt.Fprintf(r.Stdout, "dependencies: %s\n", stackID)
	}
	out := changesetOutput{
		ChangesetID:    cs.Id,
		PatchsetID:     patchset.Id,
		PatchsetNumber: patchset.Number,
		StackID:        stackID,
		WebURL:         link,
	}
	var submitErr error
	if submit.enabled {
		res, err := r.submitWhenChecksReport(ctx, opts, conn, cfg, state, submit.timeout)
		if err != nil {
			// The changeset exists either way; say which it is.
			if opts.jsonOutput() {
				_ = r.writeJSONOutput(opts, out)
			}
			return err
		}
		displaySubmitStackResponse(res)
		result := submitDependenciesOutputFromProto(res)
		out.Submit = &result
		if res.Status == "partial" {
			submitErr = userError("submit_blocked", "created "+firstNonEmpty(displayChangesetID(cs), cs.Id)+" but could not submit it", "Fix what blocks it, then run gs submit.")
		}
		if !opts.jsonOutput() && !opts.Quiet {
			r.writeSubmitResultsText(res)
		}
	}
	if opts.jsonOutput() {
		if err := r.writeJSONOutput(opts, out); err != nil {
			return err
		}
	}
	return submitErr
}

func (r Runner) requiredCommandMessage(opts commandOptions, value, prompt, message, hint string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return value, nil
	}
	if opts.Quiet || opts.NonInteractive || os.Getenv("TERM") == "dumb" || !isTerminal(r.stdin()) {
		return "", userError("message_required", message, hint)
	}
	fmt.Fprint(r.stderr(), prompt)
	line, err := bufio.NewReader(r.stdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value = strings.TrimSpace(line)
	if value == "" {
		return "", userError("message_required", message, hint)
	}
	return value, nil
}

func (r Runner) guardChangesetCreate(ctx context.Context, cfg UserConfig, state WorkspaceState, changesetClient corev1.ChangesetServiceClient) error {
	if state.CurrentChangesetID == "" {
		return nil
	}
	cs, err := changesetClient.GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: state.CurrentChangesetID})
	if err != nil {
		if grpcstatus.Code(err) == codes.NotFound {
			return nil
		}
		return err
	}
	switch cs.Status {
	case "submitted", "abandoned":
		return nil
	}
	label := firstNonEmpty(displayChangesetID(cs), storage.ShortChangesetID(state.CurrentChangesetID), state.CurrentChangesetID)
	if cs.Status == "draft" || cs.Status == "" {
		return userError(
			"workspace_changeset_exists",
			"workspace already has draft changeset "+label,
			"Run gs cs update to add a new patchset, or submit/abandon the current changeset before creating another one.",
		)
	}
	return userError(
		"workspace_changeset_exists",
		fmt.Sprintf("workspace already has changeset %s with status %s", label, cs.Status),
		"Run gs cs status "+label+" to inspect it before creating another changeset.",
	)
}
