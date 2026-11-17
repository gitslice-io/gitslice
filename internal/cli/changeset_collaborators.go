package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (r Runner) collaboratorsCommand(opts *commandOptions) *cobra.Command {
	collaboratorsCmd := &cobra.Command{
		Use:     "collaborators [changeset]",
		Aliases: []string{"collaborator"},
		Short:   "List, add or remove collaborators on a changeset",
		Args:    maxArgs(1, "gs cs collaborators [list|add|rm] ..."),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetCollaboratorsList(cmd.Context(), *opts, id)
		},
	}

	listCmd := &cobra.Command{
		Use:     "list [changeset]",
		Aliases: []string{"ls"},
		Short:   "List collaborators on a changeset",
		Args:    maxArgs(1, "gs cs collaborators list [changeset]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetCollaboratorsList(cmd.Context(), *opts, id)
		},
	}

	addCmd := &cobra.Command{
		Use:   "add [changeset] <user>",
		Short: "Add a collaborator to an open changeset",
		Args:  rangeArgs(1, 2, "gs cs collaborators add [changeset] <user>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, user := splitOptionalChangesetAndUserArgs(args)
			return r.runChangesetCollaboratorAdd(cmd.Context(), *opts, id, user)
		},
	}

	rmCmd := &cobra.Command{
		Use:     "rm [changeset] <user>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove a collaborator from an open changeset",
		Args:    rangeArgs(1, 2, "gs cs collaborators rm [changeset] <user>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, user := splitOptionalChangesetAndUserArgs(args)
			return r.runChangesetCollaboratorRemove(cmd.Context(), *opts, id, user)
		},
	}

	collaboratorsCmd.AddCommand(listCmd, addCmd, rmCmd)
	return collaboratorsCmd
}

func (r Runner) handoffCommand(opts *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "handoff [changeset] <user>",
		Short: "Hand off an open changeset to a new author, preserving the original author",
		Args:  rangeArgs(1, 2, "gs cs handoff [changeset] <user>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, user := splitOptionalChangesetAndUserArgs(args)
			return r.runChangesetHandoff(cmd.Context(), *opts, id, user)
		},
	}
}

func splitOptionalChangesetAndUserArgs(args []string) (string, string) {
	if len(args) == 1 {
		return "", args[0]
	}
	return args[0], args[1]
}

func nonNilCollaborators(cs *corev1.Changeset) []string {
	if cs == nil || len(cs.GetCollaborators()) == 0 {
		return []string{}
	}
	return append([]string{}, cs.GetCollaborators()...)
}

func (r Runner) runChangesetCollaboratorsList(ctx context.Context, opts commandOptions, requestedID string) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	cs, err := r.getChangeset(ctx, cfg, changesetID)
	if err != nil {
		return err
	}
	handle := firstNonEmpty(displayChangesetID(cs), cs.GetId())
	collaborators := nonNilCollaborators(cs)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"changeset":       handle,
			"changeset_id":    cs.GetId(),
			"author":          cs.GetAuthor(),
			"original_author": cs.GetOriginalAuthor(),
			"collaborators":   collaborators,
		})
	}
	if opts.Quiet {
		return nil
	}
	if len(collaborators) == 0 {
		fmt.Fprintln(r.Stdout, "no collaborators")
		return nil
	}
	for _, collab := range collaborators {
		fmt.Fprintln(r.Stdout, collab)
	}
	return nil
}

func (r Runner) runChangesetCollaboratorAdd(ctx context.Context, opts commandOptions, requestedID, user string) error {
	user = strings.TrimSpace(user)
	if user == "" {
		return userError("invalid_args", "user is required", "Pass a username or subject id.")
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
	cs, err := corev1.NewChangesetServiceClient(conn).AddChangesetCollaborator(authContext(ctx, cfg), &corev1.AddChangesetCollaboratorRequest{
		ChangesetId: changesetID,
		Username:    user,
	})
	if err != nil {
		return err
	}
	handle := firstNonEmpty(displayChangesetID(cs), cs.GetId())
	collaborators := nonNilCollaborators(cs)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"changeset":     handle,
			"changeset_id":  cs.GetId(),
			"collaborators": collaborators,
		})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "added %s as collaborator on %s\n", user, handle)
	}
	return nil
}

func (r Runner) runChangesetCollaboratorRemove(ctx context.Context, opts commandOptions, requestedID, user string) error {
	user = strings.TrimSpace(user)
	if user == "" {
		return userError("invalid_args", "user is required", "Pass a username or subject id.")
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
	cs, err := corev1.NewChangesetServiceClient(conn).RemoveChangesetCollaborator(authContext(ctx, cfg), &corev1.RemoveChangesetCollaboratorRequest{
		ChangesetId: changesetID,
		Username:    user,
	})
	if err != nil {
		return err
	}
	handle := firstNonEmpty(displayChangesetID(cs), cs.GetId())
	collaborators := nonNilCollaborators(cs)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"changeset":     handle,
			"changeset_id":  cs.GetId(),
			"collaborators": collaborators,
		})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "removed %s from collaborators on %s\n", user, handle)
	}
	return nil
}

func (r Runner) runChangesetHandoff(ctx context.Context, opts commandOptions, requestedID, user string) error {
	user = strings.TrimSpace(user)
	if user == "" {
		return userError("invalid_args", "user is required", "Pass a username or subject id.")
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
	cs, err := corev1.NewChangesetServiceClient(conn).HandoffChangeset(authContext(ctx, cfg), &corev1.HandoffChangesetRequest{
		ChangesetId: changesetID,
		Username:    user,
	})
	if err != nil {
		return err
	}
	handle := firstNonEmpty(displayChangesetID(cs), cs.GetId())
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"changeset":       handle,
			"changeset_id":    cs.GetId(),
			"author":          cs.GetAuthor(),
			"original_author": cs.GetOriginalAuthor(),
		})
	}
	if !opts.Quiet {
		if cs.GetOriginalAuthor() != "" {
			fmt.Fprintf(r.Stdout, "handed off %s to %s (original author: %s)\n", handle, cs.GetAuthor(), cs.GetOriginalAuthor())
		} else {
			fmt.Fprintf(r.Stdout, "handed off %s to %s\n", handle, cs.GetAuthor())
		}
	}
	return nil
}
