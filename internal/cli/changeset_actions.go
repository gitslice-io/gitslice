package cli

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// runChangesetLink prints a changeset's web link after checking that the
// changeset exists, so a link handed to a person always opens. Given a commit
// id by mistake, it says so instead of printing a link that would 404.
func (r Runner) runChangesetLink(ctx context.Context, opts commandOptions, requestedID string) error {
	requestedID = strings.TrimSpace(requestedID)
	return r.withChangesetSession(ctx, requestedID, func(s *Session, target ChangesetTarget) error {
		// The server also accepts the id of a commit a changeset landed as, and
		// answers with that changeset.
		cs, err := s.GetChangeset(target.ChangesetID)
		if err != nil {
			if grpcstatus.Code(err) == codes.NotFound {
				return userError("changeset_not_found", "no changeset or landed commit "+target.ChangesetID, "Check the id with gs cs list.")
			}
			return err
		}
		handle := firstNonEmpty(displayChangesetID(cs), cs.Id)
		link := webResourceURL("/cs/" + handle)
		_, commitShaped := model.CommitIDLookupPrefix(target.ChangesetID)
		fromCommit := commitShaped && !sameChangesetSelector(target.ChangesetID, cs.Id)
		if fromCommit && !opts.Quiet && !opts.JSONOutput() {
			fmt.Fprintf(r.Stderr, "%s is the commit changeset %s landed as; this is the changeset's link.\n", target.ChangesetID, handle)
		}
		if opts.JSONOutput() {
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
	})
}

func (r Runner) runChangesetApprove(ctx context.Context, opts commandOptions, requestedID string) error {
	return r.withChangesetSession(ctx, requestedID, func(s *Session, target ChangesetTarget) error {
		res, err := s.Changeset().ApproveChangeset(s.Ctx(), &corev1.ApproveChangesetRequest{ChangesetId: target.ChangesetID})
		if err != nil {
			return err
		}
		if opts.JSONOutput() {
			return r.writeJSONOutput(opts, res)
		}
		if opts.Quiet {
			return nil
		}
		fmt.Fprintf(r.Stdout, "approved patchset %s for changeset %s\n", res.PatchsetId, requestedID)
		return nil
	})
}

func (r Runner) runChangesetCheck(ctx context.Context, opts commandOptions, requestedID, checkName, resultStatus string) error {
	if _, ok := model.NormalizeCheckStatus(resultStatus); !ok {
		return userError("invalid_args", "check status must be pass or fail", "Pass --status pass or --status fail.")
	}
	return r.withChangesetSession(ctx, requestedID, func(s *Session, target ChangesetTarget) error {
		res, err := s.Changeset().ReportCheckResult(s.Ctx(), &corev1.ReportCheckResultRequest{
			ChangesetId: target.ChangesetID,
			CheckName:   checkName,
			Status:      resultStatus,
		})
		if err != nil {
			return err
		}
		if opts.JSONOutput() {
			return r.writeJSONOutput(opts, res)
		}
		if opts.Quiet {
			return nil
		}
		fmt.Fprintf(r.Stdout, "reported check %s=%s for changeset %s\n", res.CheckName, res.Status, requestedID)
		return nil
	})
}

func (r Runner) runChangesetAbandon(ctx context.Context, opts commandOptions, requestedID, reason string) error {
	return r.withChangesetSession(ctx, requestedID, func(s *Session, target ChangesetTarget) error {
		cs, err := s.GetChangeset(target.ChangesetID)
		if err != nil {
			return err
		}
		_, err = s.Changeset().AbandonChangeset(s.Ctx(), &corev1.AbandonChangesetRequest{
			ChangesetId: target.ChangesetID,
			Reason:      reason,
		})
		if err != nil {
			return err
		}
		if target.UsingWorkspaceCurrent && target.HasWorkspace {
			state := target.State
			state.CurrentChangesetID = ""
			state.CurrentPatchsetID = ""
			if err := r.writeWorkspaceState(state); err != nil {
				return err
			}
		}
		if opts.JSONOutput() {
			return r.writeJSONOutput(opts, changesetOutput{ChangesetID: cs.Id, Status: "abandoned"})
		}
		if opts.Quiet {
			return nil
		}
		fmt.Fprintf(r.Stdout, "abandoned changeset %s\n", firstNonEmpty(displayChangesetID(cs), target.ChangesetID))
		return nil
	})
}
