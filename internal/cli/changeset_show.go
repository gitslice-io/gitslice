package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type changesetOutput struct {
	ChangesetID         string                     `json:"changeset_id"`
	PatchsetID          string                     `json:"patchset_id,omitempty"`
	PatchsetNumber      int64                      `json:"patchset_number,omitempty"`
	Status              string                     `json:"status,omitempty"`
	SubmitBlockedReason string                     `json:"submit_blocked_reason,omitempty"`
	SubmitRequirements  *corev1.SubmitRequirements `json:"submit_requirements,omitempty"`
	StackID             string                     `json:"dependency_id,omitempty"`
	WebURL              string                     `json:"web_url,omitempty"`
	// Submit is what gs create --submit did with the changeset.
	Submit *submitDependenciesOutput `json:"submit,omitempty"`
	// Checks are the current patchset's check runs (gs cs status), the
	// latest attempt of each.
	Checks []changesetCheckOutput `json:"checks,omitempty"`
}

type changesetCheckOutput struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	RunID  string `json:"run_id"`
	// Daemon is the name of the daemon that ran the check, if one did.
	Daemon string `json:"daemon,omitempty"`
}

// currentPatchsetChecks lists the latest run of each check on the patchset.
// A server or caller that cannot list them gets none.
func currentPatchsetChecks(ctx context.Context, conn *grpc.ClientConn, changesetID, patchsetID string) []changesetCheckOutput {
	if patchsetID == "" {
		return nil
	}
	res, err := corev1.NewCheckServiceClient(conn).ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: changesetID, PatchsetId: patchsetID})
	if err != nil {
		return nil
	}
	latest := map[string]*corev1.CheckRun{}
	for _, run := range res.GetRuns() {
		if run.GetPatchsetId() != patchsetID {
			continue
		}
		if prev := latest[run.GetCheckName()]; prev == nil || run.GetAttempt() > prev.GetAttempt() || (run.GetAttempt() == prev.GetAttempt() && run.GetCreatedAt() > prev.GetCreatedAt()) {
			latest[run.GetCheckName()] = run
		}
	}
	out := make([]changesetCheckOutput, 0, len(latest))
	for name, run := range latest {
		out = append(out, changesetCheckOutput{Name: name, Status: run.GetStatus(), RunID: run.GetId(), Daemon: run.GetDaemonName()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r Runner) runChangesetStatus(ctx context.Context, opts commandOptions, requestedID string, watch bool, watchTimeout time.Duration) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	changesetClient := corev1.NewChangesetServiceClient(conn)
	cs, err := changesetClient.GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
	if err != nil {
		return err
	}
	if watch && cs.Status != "submitted" {
		if _, _, err := r.waitForChangesetPublished(ctx, conn, cfg, changesetID, displayChangesetID(cs), watchTimeout, !opts.Quiet && !opts.jsonOutput()); err != nil {
			return err
		}
		cs, err = changesetClient.GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
	}
	if err != nil {
		return err
	}
	output := changesetOutput{
		ChangesetID:         cs.Id,
		PatchsetID:          cs.CurrentPatchsetId,
		PatchsetNumber:      cs.CurrentPatchsetNumber,
		Status:              cs.Status,
		SubmitBlockedReason: displaySubmitBlockedReason(cs.SubmitBlockedReason),
		SubmitRequirements:  currentPatchsetSubmitRequirements(cs),
	}
	if cs.Status != "submitted" && cs.Status != "abandoned" {
		output.Checks = currentPatchsetChecks(authContext(ctx, cfg), conn, cs.Id, cs.CurrentPatchsetId)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, output)
	}
	if opts.Quiet {
		if cs.Status == "submitted" {
			return nil
		}
		return userError("changeset_not_submitted", "changeset is not submitted", "Run gs cs status without --quiet for details.")
	}
	fmt.Fprintf(r.Stdout, "changeset: %s\nstatus: %s\n", firstNonEmpty(displayChangesetID(cs), cs.Id), cs.Status)
	if cs.SubmitBlockedReason != "" {
		fmt.Fprintf(r.Stdout, "submit_blocked_reason: %s\n", displaySubmitBlockedReason(cs.SubmitBlockedReason))
	}
	if cs.CurrentPatchsetNumber > 0 {
		fmt.Fprintf(r.Stdout, "patchset: %d\n", cs.CurrentPatchsetNumber)
	}
	printSubmitRequirements(r.Stdout, currentPatchsetSubmitRequirements(cs))
	if len(output.Checks) > 0 {
		fmt.Fprintln(r.Stdout, "checks:")
		for _, check := range output.Checks {
			if check.Daemon != "" {
				fmt.Fprintf(r.Stdout, "  %s: %s (on %s)\n", check.Name, check.Status, check.Daemon)
				continue
			}
			fmt.Fprintf(r.Stdout, "  %s: %s\n", check.Name, check.Status)
		}
	}
	return nil
}

func (r Runner) runChangesetShow(ctx context.Context, opts commandOptions, requestedID string) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	cs, err := r.getChangeset(ctx, cfg, changesetID)
	if err != nil {
		return err
	}
	cs.SubmitBlockedReason = displaySubmitBlockedReason(cs.SubmitBlockedReason)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, cs)
	}
	if opts.Quiet {
		return nil
	}
	printChangesetDetails(r.Stdout, cs, false)
	return nil
}

type patchsetConversationView struct {
	Patchset       int64                       `json:"patchset"`
	PatchsetHandle string                      `json:"patchset_handle,omitempty"`
	ConversationID string                      `json:"conversation_id,omitempty"`
	Events         []*corev1.ConversationEvent `json:"events,omitempty"`
}

func (r Runner) runChangesetConversation(ctx context.Context, opts commandOptions, requestedID, patchsetSel string) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	cs, err := r.getChangeset(ctx, cfg, changesetID)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	agent := corev1.NewAgentServiceClient(conn)
	callCtx := authContext(ctx, cfg)

	patchsetSel = strings.TrimSpace(patchsetSel)
	// Per conversation, each patchset's exchange is the events after the previous
	// patchset's cutoff up to this one's. Walk all patchsets in order so cutoffs
	// stay correct even when a single patchset is selected.
	prevByConv := map[string]int64{}
	views := make([]patchsetConversationView, 0, len(cs.Patchsets))
	for _, ps := range cs.Patchsets {
		convID := ps.GetAuthoringConversationId()
		after := prevByConv[convID]
		before := ps.GetAuthoringConversationSeq()
		if convID != "" {
			prevByConv[convID] = before
		}
		if patchsetSel != "" && fmt.Sprintf("%d", ps.GetNumber()) != patchsetSel {
			continue
		}
		view := patchsetConversationView{Patchset: ps.GetNumber(), PatchsetHandle: ps.GetHandle(), ConversationID: convID}
		if convID != "" {
			res, err := agent.GetConversationEvents(callCtx, &corev1.GetConversationEventsRequest{
				ConversationId: convID,
				AfterSeq:       after,
				BeforeSeq:      before,
			})
			if err != nil {
				return err
			}
			view.Events = res.GetEvents()
		}
		views = append(views, view)
	}

	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"changeset_id": cs.GetId(), "patchsets": views})
	}
	if opts.Quiet {
		return nil
	}
	if len(views) == 0 {
		fmt.Fprintln(r.Stdout, "no patchsets")
		return nil
	}
	for _, view := range views {
		label := fmt.Sprintf("patchset %d", view.Patchset)
		if view.PatchsetHandle != "" {
			label = view.PatchsetHandle
		}
		if view.ConversationID == "" {
			fmt.Fprintf(r.Stdout, "%s: (no agent conversation)\n", label)
			continue
		}
		fmt.Fprintf(r.Stdout, "%s  conversation %s\n", label, view.ConversationID)
		if len(view.Events) == 0 {
			fmt.Fprintln(r.Stdout, "  (no messages)")
		}
		for _, ev := range view.Events {
			fmt.Fprintf(r.Stdout, "  [%s/%s] %s\n", ev.GetRole(), ev.GetType(), ev.GetText())
		}
		fmt.Fprintln(r.Stdout)
	}
	return nil
}

func (r Runner) runChangesetExplain(ctx context.Context, opts commandOptions, requestedID string) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	cs, err := r.getChangeset(ctx, cfg, changesetID)
	if err != nil {
		return err
	}
	cs.SubmitBlockedReason = displaySubmitBlockedReason(cs.SubmitBlockedReason)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, cs)
	}
	if opts.Quiet {
		return nil
	}
	printChangesetDetails(r.Stdout, cs, true)
	return nil
}

func (r Runner) runChangesetVersions(ctx context.Context, opts commandOptions, requestedID string) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	cs, err := r.getChangeset(ctx, cfg, changesetID)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"changeset_id": cs.Id,
			"patchsets":    cs.Patchsets,
		})
	}
	if opts.Quiet {
		return nil
	}
	color := r.colorEnabled(opts)
	fmt.Fprintf(r.Stdout, "%s %s\n", colorize(color, ansiDim, "changeset:"), colorize(color, ansiGreen, firstNonEmpty(displayChangesetID(cs), cs.Id)))
	printPatchsetsColor(r.Stdout, cs, color)
	return nil
}

func (r Runner) runChangesetDiff(ctx context.Context, opts commandOptions, requestedID, patchset, from, to string, nameOnly, stat bool) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewChangesetServiceClient(conn).DiffChangeset(authContext(ctx, cfg), &corev1.DiffChangesetRequest{
		ChangesetId:  changesetID,
		Patchset:     strings.TrimSpace(patchset),
		FromPatchset: strings.TrimSpace(from),
		ToPatchset:   strings.TrimSpace(to),
	})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, res)
	}
	if opts.Quiet {
		return nil
	}
	if nameOnly {
		printPathListColor(r.Stdout, res.ChangedPaths, r.colorEnabled(opts))
		return nil
	}
	if stat {
		printPathStatColor(r.Stdout, res.ChangedPaths, r.colorEnabled(opts))
		return nil
	}
	_, err = io.WriteString(r.Stdout, colorUnifiedDiff(compactUnifiedDiff(res.Diff, opts.diffContext()), r.colorEnabled(opts)))
	return err
}

func (r Runner) runChangesetList(ctx context.Context, opts commandOptions, sliceRef, statusFilter string, limit int) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	var ref *corev1.SliceRef
	if strings.TrimSpace(sliceRef) != "" {
		ref, err = r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
		if err != nil {
			return err
		}
	} else {
		ws, err := r.readWorkspaceConfig()
		if err != nil {
			if isUserErrorCode(err, "not_in_workspace") {
				return userError("missing_slice", "changeset list needs a slice outside a workspace", "Run gs cs list --slice <slice|account:slice>.")
			}
			return err
		}
		ref = &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}
	}
	res, err := corev1.NewChangesetServiceClient(conn).ListChangesets(callCtx, &corev1.ListChangesetsRequest{
		AuthoringSlice: ref,
		Status:         strings.TrimSpace(statusFilter),
		Limit:          int32(limit),
		// The text list shows each changeset's own fields; --json keeps the
		// full answer, patchsets included, for scripts that read them.
		Summary: !opts.jsonOutput(),
	})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, res)
	}
	if opts.Quiet {
		return nil
	}
	label := ref.Account + ":" + ref.Slice
	if statusFilter != "" {
		fmt.Fprintf(r.Stdout, "changesets for %s status=%s:\n", label, statusFilter)
	} else {
		fmt.Fprintf(r.Stdout, "changesets for %s:\n", label)
	}
	if len(res.Changesets) == 0 {
		fmt.Fprintln(r.Stdout, "  none")
		return nil
	}
	for _, cs := range res.Changesets {
		printChangesetOneLine(r.Stdout, cs)
	}
	return nil
}

func (r Runner) resolveChangesetCommandState(requestedID string) (UserConfig, WorkspaceConfig, WorkspaceState, bool, string, bool, error) {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID == "" {
		cfg, ws, state, err := r.loadLocalState()
		if err != nil {
			return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, false, "", false, err
		}
		if state.CurrentChangesetID == "" {
			return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, false, "", false, userError("no_current_changeset", "no current changeset in workspace", "Run gs cs create first or pass a changeset id.")
		}
		return cfg, ws, state, true, state.CurrentChangesetID, true, nil
	}
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, false, "", false, err
	}
	var ws WorkspaceConfig
	var state WorkspaceState
	hasWorkspace := false
	usingWorkspaceCurrent := false
	if workspaceCfg, err := r.readWorkspaceConfig(); err == nil {
		ws = workspaceCfg
		state, err = r.readWorkspaceState()
		if err != nil {
			return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, false, "", false, err
		}
		hasWorkspace = true
		usingWorkspaceCurrent = requestedID == state.CurrentChangesetID || sameChangesetSelector(requestedID, state.CurrentChangesetID)
	} else if !isUserErrorCode(err, "not_in_workspace") {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, false, "", false, err
	}
	return cfg, ws, state, hasWorkspace, requestedID, usingWorkspaceCurrent, nil
}

func (r Runner) getChangeset(ctx context.Context, cfg UserConfig, changesetID string) (*corev1.Changeset, error) {
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return corev1.NewChangesetServiceClient(conn).GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
}
