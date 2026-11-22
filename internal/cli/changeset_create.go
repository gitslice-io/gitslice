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
	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func (r Runner) runChangesetCreate(ctx context.Context, opts commandOptions, title string, empty emptyChangesetOptions, review reviewOptions) error {
	if err := empty.validate(); err != nil {
		return err
	}
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	conn, err := r.dialServer(ctx, cfg.ServerAddr)
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
		TargetRef:      model.DefaultTargetRef,
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
	warnings := r.collectPathOverlapWarnings(
		ctx,
		conn,
		cfg,
		&corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
		patchset.GetChangedPaths(),
		[]string{cs.Id},
	)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, changesetOutput{
			ChangesetID:     cs.Id,
			PatchsetID:      patchset.Id,
			PatchsetNumber:  patchset.Number,
			WebURL:          webURL,
			OverlapWarnings: warnings,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "created changeset %s patchset %d\n", label, patchset.Number)
	if webURL != "" {
		fmt.Fprintf(r.Stdout, "view: %s\n", webURL)
	}
	printOverlapWarnings(r.Stdout, warnings)
	return nil
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
	conn, err := r.dialServer(ctx, cfg.ServerAddr)
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
			TargetRef:      model.DefaultTargetRef,
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
	var activeStack *corev1.ChangesetStack
	if stackID != "" {
		activeStack, _ = corev1.NewChangesetStackServiceClient(conn).GetStack(callCtx, &corev1.GetStackRequest{StackId: stackID})
	}
	warnings := r.collectPathOverlapWarnings(
		ctx,
		conn,
		cfg,
		&corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
		patchset.GetChangedPaths(),
		excludeChangesetIDsFromStack(cs.Id, activeStack),
	)
	if !opts.jsonOutput() && !opts.Quiet {
		fmt.Fprintf(r.Stdout, "created %s patchset %d\n", firstNonEmpty(displayChangesetID(cs), cs.Id), patchset.Number)
		if link != "" {
			fmt.Fprintf(r.Stdout, "view: %s\n", link)
		}
		fmt.Fprintf(r.Stdout, "dependencies: %s\n", stackID)
		printOverlapWarnings(r.Stdout, warnings)
	}
	out := changesetOutput{
		ChangesetID:     cs.Id,
		PatchsetID:      patchset.Id,
		PatchsetNumber:  patchset.Number,
		StackID:         stackID,
		WebURL:          link,
		OverlapWarnings: warnings,
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
		r.reportFailingChecks(ctx, conn, cfg, opts, res)
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
	label := firstNonEmpty(displayChangesetID(cs), model.ShortChangesetID(state.CurrentChangesetID), state.CurrentChangesetID)
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
