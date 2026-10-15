package cli

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// runChangesetLink prints a changeset's web link after checking that the
// changeset exists, so a link handed to a person always opens. Given a commit
// id by mistake, it says so instead of printing a link that would 404.
func (r Runner) runChangesetLink(ctx context.Context, opts commandOptions, requestedID string) error {
	requestedID = strings.TrimSpace(requestedID)
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	// The server also accepts the id of a commit a changeset landed as, and
	// answers with that changeset.
	cs, err := r.getChangeset(ctx, cfg, changesetID)
	if err != nil {
		if grpcstatus.Code(err) == codes.NotFound {
			return userError("changeset_not_found", "no changeset or landed commit "+changesetID, "Check the id with gs cs list.")
		}
		return err
	}
	handle := firstNonEmpty(displayChangesetID(cs), cs.Id)
	link := webResourceURL("/cs/" + handle)
	_, commitShaped := storage.CommitIDLookupPrefix(changesetID)
	fromCommit := commitShaped && !sameChangesetSelector(changesetID, cs.Id)
	if fromCommit && !opts.Quiet && !opts.jsonOutput() {
		fmt.Fprintf(r.Stderr, "%s is the commit changeset %s landed as; this is the changeset's link.\n", changesetID, handle)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"changeset_id":  cs.Id,
			"changeset":     handle,
			"changeset_url": link,
			"title":         cs.Title,
			"status":        cs.Status,
			"from_commit":   fromCommit,
		})
	}
	if !opts.Quiet {
		fmt.Fprintln(r.Stdout, link)
	}
	return nil
}

func (r Runner) runChangesetApprove(ctx context.Context, opts commandOptions, requestedID string) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewChangesetServiceClient(conn).ApproveChangeset(authContext(ctx, cfg), &corev1.ApproveChangesetRequest{ChangesetId: changesetID})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, res)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "approved patchset %s for changeset %s\n", res.PatchsetId, requestedID)
	return nil
}

func (r Runner) runChangesetCheck(ctx context.Context, opts commandOptions, requestedID, checkName, resultStatus string) error {
	if _, ok := storage.NormalizeCheckStatus(resultStatus); !ok {
		return userError("invalid_args", "check status must be pass or fail", "Pass --status pass or --status fail.")
	}
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewChangesetServiceClient(conn).ReportCheckResult(authContext(ctx, cfg), &corev1.ReportCheckResultRequest{
		ChangesetId: changesetID,
		CheckName:   checkName,
		Status:      resultStatus,
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
	fmt.Fprintf(r.Stdout, "reported check %s=%s for changeset %s\n", res.CheckName, res.Status, requestedID)
	return nil
}

func (r Runner) runChangesetAbandon(ctx context.Context, opts commandOptions, requestedID, reason string) error {
	cfg, _, state, hasWorkspace, changesetID, usingWorkspaceCurrent, err := r.resolveChangesetCommandState(requestedID)
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
	_, err = changesetClient.AbandonChangeset(authContext(ctx, cfg), &corev1.AbandonChangesetRequest{
		ChangesetId: changesetID,
		Reason:      reason,
	})
	if err != nil {
		return err
	}
	if usingWorkspaceCurrent && hasWorkspace {
		state.CurrentChangesetID = ""
		state.CurrentPatchsetID = ""
		if err := r.writeWorkspaceState(state); err != nil {
			return err
		}
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, changesetOutput{ChangesetID: cs.Id, Status: "abandoned"})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "abandoned changeset %s\n", firstNonEmpty(displayChangesetID(cs), changesetID))
	return nil
}
