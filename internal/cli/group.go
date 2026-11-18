package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

type groupMemberOutput struct {
	Username    string `json:"username"`
	SubjectID   string `json:"subject_id"`
	DisplayName string `json:"display_name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	AddedBy     string `json:"added_by,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type groupOutput struct {
	ID          string              `json:"id,omitempty"`
	Account     string              `json:"account"`
	Name        string              `json:"name"`
	Handle      string              `json:"handle"`
	Description string              `json:"description,omitempty"`
	CreatedBy   string              `json:"created_by,omitempty"`
	CreatedAt   string              `json:"created_at,omitempty"`
	UpdatedAt   string              `json:"updated_at,omitempty"`
	MemberCount int32               `json:"member_count"`
	Members     []groupMemberOutput `json:"members"`
}

func (r Runner) groupCommand(opts *commandOptions) *cobra.Command {
	groupCmd := &cobra.Command{
		Use:   "group",
		Short: "Create and manage account groups (@account/name)",
		RunE:  requireSubcommand("group"),
	}

	var createDescription string
	createCmd := &cobra.Command{
		Use:   "create <@account/name>",
		Short: "Create an account group (account owner or admin only)",
		Args:  exactArgs(1, "gs group create <@account/name> [--description text]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runGroupCreate(cmd.Context(), *opts, args[0], createDescription)
		},
	}
	createCmd.Flags().StringVar(&createDescription, "description", "", "optional group description (up to 500 characters)")

	listCmd := &cobra.Command{
		Use:   "list [account]",
		Short: "List groups in an account",
		Args:  maxArgs(1, "gs group list [account]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			account := ""
			if len(args) == 1 {
				account = args[0]
			}
			return r.runGroupList(cmd.Context(), *opts, account)
		},
	}

	showCmd := &cobra.Command{
		Use:   "show <@account/name>",
		Short: "Show a group and its members",
		Args:  exactArgs(1, "gs group show <@account/name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runGroupShow(cmd.Context(), *opts, args[0])
		},
	}

	deleteCmd := &cobra.Command{
		Use:   "delete <@account/name>",
		Short: "Delete an account group (account owner or admin only)",
		Args:  exactArgs(1, "gs group delete <@account/name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runGroupDelete(cmd.Context(), *opts, args[0])
		},
	}

	addCmd := &cobra.Command{
		Use:   "add <@account/name> <username>",
		Short: "Add a user or agent to a group (account owner or admin only)",
		Args:  exactArgs(2, "gs group add <@account/name> <username>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runGroupAddMember(cmd.Context(), *opts, args[0], args[1])
		},
	}

	rmCmd := &cobra.Command{
		Use:     "rm <@account/name> <username>",
		Aliases: []string{"remove"},
		Short:   "Remove a user or agent from a group (account owner or admin only)",
		Args:    exactArgs(2, "gs group rm <@account/name> <username>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runGroupRemoveMember(cmd.Context(), *opts, args[0], args[1])
		},
	}

	groupCmd.AddCommand(createCmd, listCmd, showCmd, deleteCmd, addCmd, rmCmd)
	return groupCmd
}

func (r Runner) runGroupCreate(ctx context.Context, opts commandOptions, rawGroup, description string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	account, name, err := r.resolveCLIGroupTarget(callCtx, cfg, conn, rawGroup)
	if err != nil {
		return err
	}
	resp, err := corev1.NewAuthServiceClient(conn).CreateGroup(callCtx, &corev1.CreateGroupRequest{
		Account:     account,
		Name:        name,
		Description: description,
	})
	if err != nil {
		return groupError(err)
	}
	out := groupFromProto(resp)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "created group %s\n", out.Handle)
	}
	return nil
}

func (r Runner) runGroupList(ctx context.Context, opts commandOptions, rawAccount string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	account := strings.TrimPrefix(strings.TrimSpace(rawAccount), "@")
	if account == "" {
		account, err = r.defaultSliceAccount(callCtx, cfg, conn)
		if err != nil {
			return err
		}
	}
	resp, err := corev1.NewAuthServiceClient(conn).ListGroups(callCtx, &corev1.ListGroupsRequest{
		Account: account,
	})
	if err != nil {
		return groupError(err)
	}
	groups := make([]groupOutput, 0, len(resp.GetGroups()))
	for _, g := range resp.GetGroups() {
		groups = append(groups, groupFromProto(g))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"account": resp.GetAccount(),
			"groups":  groups,
		})
	}
	if opts.Quiet {
		return nil
	}
	if len(groups) == 0 {
		fmt.Fprintf(r.Stdout, "no groups in %s\n", resp.GetAccount())
		return nil
	}
	for _, g := range groups {
		noun := "members"
		if g.MemberCount == 1 {
			noun = "member"
		}
		if g.Description != "" {
			fmt.Fprintf(r.Stdout, "%s\t%d %s\t%s\n", g.Handle, g.MemberCount, noun, g.Description)
		} else {
			fmt.Fprintf(r.Stdout, "%s\t%d %s\n", g.Handle, g.MemberCount, noun)
		}
	}
	return nil
}

func (r Runner) runGroupShow(ctx context.Context, opts commandOptions, rawGroup string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	account, name, err := r.resolveCLIGroupTarget(callCtx, cfg, conn, rawGroup)
	if err != nil {
		return err
	}
	resp, err := corev1.NewAuthServiceClient(conn).GetGroup(callCtx, &corev1.GetGroupRequest{
		Account: account,
		Name:    name,
	})
	if err != nil {
		return groupError(err)
	}
	out := groupFromProto(resp)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintln(r.Stdout, out.Handle)
	if out.Description != "" {
		fmt.Fprintf(r.Stdout, "description: %s\n", out.Description)
	}
	fmt.Fprintf(r.Stdout, "members (%d):\n", out.MemberCount)
	if len(out.Members) == 0 {
		fmt.Fprintln(r.Stdout, "  (none)")
		return nil
	}
	for _, m := range out.Members {
		label := m.Username
		if label == "" {
			label = m.SubjectID
		}
		var notes []string
		if m.DisplayName != "" {
			notes = append(notes, m.DisplayName)
		}
		if m.Kind != "" {
			notes = append(notes, m.Kind)
		}
		if len(notes) > 0 {
			fmt.Fprintf(r.Stdout, "  %s\t(%s)\n", label, strings.Join(notes, ", "))
		} else {
			fmt.Fprintf(r.Stdout, "  %s\n", label)
		}
	}
	return nil
}

func (r Runner) runGroupDelete(ctx context.Context, opts commandOptions, rawGroup string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	account, name, err := r.resolveCLIGroupTarget(callCtx, cfg, conn, rawGroup)
	if err != nil {
		return err
	}
	if _, err := corev1.NewAuthServiceClient(conn).DeleteGroup(callCtx, &corev1.DeleteGroupRequest{
		Account: account,
		Name:    name,
	}); err != nil {
		return groupError(err)
	}
	handle := storage.FormatGroupHandle(account, name)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"account": account,
			"name":    name,
			"handle":  handle,
			"deleted": true,
		})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "deleted group %s\n", handle)
	}
	return nil
}

func (r Runner) runGroupAddMember(ctx context.Context, opts commandOptions, rawGroup, rawUsername string) error {
	username := strings.TrimPrefix(strings.TrimSpace(rawUsername), "@")
	if username == "" {
		return userError("invalid_username", "username is required", "Pass the username of an existing user or agent.")
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	account, name, err := r.resolveCLIGroupTarget(callCtx, cfg, conn, rawGroup)
	if err != nil {
		return err
	}
	resp, err := corev1.NewAuthServiceClient(conn).AddGroupMember(callCtx, &corev1.AddGroupMemberRequest{
		Account:  account,
		Name:     name,
		Username: username,
	})
	if err != nil {
		return groupError(err)
	}
	out := groupFromProto(resp)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "added %s to %s\n", username, out.Handle)
	}
	return nil
}

func (r Runner) runGroupRemoveMember(ctx context.Context, opts commandOptions, rawGroup, rawUsername string) error {
	username := strings.TrimPrefix(strings.TrimSpace(rawUsername), "@")
	if username == "" {
		return userError("invalid_username", "username is required", "Pass the username of the group member to remove.")
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	account, name, err := r.resolveCLIGroupTarget(callCtx, cfg, conn, rawGroup)
	if err != nil {
		return err
	}
	resp, err := corev1.NewAuthServiceClient(conn).RemoveGroupMember(callCtx, &corev1.RemoveGroupMemberRequest{
		Account:  account,
		Name:     name,
		Username: username,
	})
	if err != nil {
		return groupError(err)
	}
	out := groupFromProto(resp)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "removed %s from %s\n", username, out.Handle)
	}
	return nil
}

func (r Runner) resolveCLIGroupTarget(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, raw string) (string, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", userError("invalid_group", "group reference is required", "Use @account/name, for example @acme/api.")
	}
	if strings.HasPrefix(trimmed, "@") || strings.Contains(trimmed, "/") {
		ref := trimmed
		if !strings.HasPrefix(ref, "@") {
			ref = "@" + ref
		}
		account, name, err := storage.ParseGroupRefStrict(ref)
		if err != nil {
			return "", "", userError("invalid_group", err.Error(), "Use @account/name, for example @acme/api.")
		}
		return account, name, nil
	}
	name, err := storage.NormalizeGroupName(trimmed)
	if err != nil {
		return "", "", userError("invalid_group", err.Error(), "Use @account/name, for example @acme/api.")
	}
	account, err := r.defaultSliceAccount(ctx, cfg, conn)
	if err != nil {
		return "", "", err
	}
	return account, name, nil
}

func groupFromProto(g *corev1.AccountGroup) groupOutput {
	if g == nil {
		return groupOutput{Members: []groupMemberOutput{}}
	}
	members := make([]groupMemberOutput, 0, len(g.GetMembers()))
	for _, m := range g.GetMembers() {
		members = append(members, groupMemberOutput{
			Username:    m.GetUsername(),
			SubjectID:   m.GetSubjectId(),
			DisplayName: m.GetDisplayName(),
			Kind:        m.GetKind(),
			AddedBy:     m.GetAddedBy(),
			CreatedAt:   m.GetCreatedAt(),
		})
	}
	count := g.GetMemberCount()
	if count == 0 && len(members) > 0 {
		count = int32(len(members))
	}
	return groupOutput{
		ID:          g.GetId(),
		Account:     g.GetAccount(),
		Name:        g.GetName(),
		Handle:      g.GetHandle(),
		Description: g.GetDescription(),
		CreatedBy:   g.GetCreatedBy(),
		CreatedAt:   g.GetCreatedAt(),
		UpdatedAt:   g.GetUpdatedAt(),
		MemberCount: count,
		Members:     members,
	}
}

var groupErrorHints = domainErrorOverrides{
	codes.PermissionDenied: {
		Hint: "Account groups are managed by account owners and admins.",
	},
	codes.NotFound: {
		Hint: "Check the @account/name group handle and username.",
	},
	codes.AlreadyExists: {
		Hint: "Pick another group name.",
	},
}

func groupError(err error) error {
	return withDomainErrorHints(err, groupErrorHints)
}
