package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type accountMemberOutput struct {
	Username  string `json:"username"`
	SubjectID string `json:"subject_id"`
	Role      string `json:"role"`
}

type accountMembersOutput struct {
	Account string                `json:"account"`
	Kind    string                `json:"kind,omitempty"`
	Members []accountMemberOutput `json:"members"`
}

// accountCommand groups organization account management.
func (r Runner) accountCommand(opts *commandOptions) *cobra.Command {
	accountCmd := &cobra.Command{
		Use:   "account",
		Short: "Manage organization accounts and their members",
		RunE:  requireSubcommand("account"),
	}
	var owners []string
	createOrgCmd := &cobra.Command{
		Use:   "create-org <slug>",
		Short: "Create an organization account (server operators only)",
		Args:  exactArgs(1, "gs account create-org <slug> [--owner <username>]..."),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountCreateOrg(cmd.Context(), *opts, args[0], owners)
		},
	}
	createOrgCmd.Flags().StringArrayVar(&owners, "owner", nil, "username of an initial owner; repeat for several (default: you)")
	membersCmd := &cobra.Command{
		Use:   "members <account>",
		Short: "List an account's members and their roles",
		Args:  exactArgs(1, "gs account members <account>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountMembers(cmd.Context(), *opts, args[0])
		},
	}
	role := ""
	setMemberCmd := &cobra.Command{
		Use:   "set-member <account> <username>",
		Short: "Add a member to an organization or change their role",
		Args:  exactArgs(2, "gs account set-member <account> <username> --role owner|admin|writer|member|reader"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountSetMember(cmd.Context(), *opts, args[0], args[1], role)
		},
	}
	setMemberCmd.Flags().StringVar(&role, "role", role, "role: owner, admin, writer, member or reader")
	removeYes := false
	removeMemberCmd := &cobra.Command{
		Use:   "remove-member <account> <username>",
		Short: "Remove a member from an organization",
		Args:  exactArgs(2, "gs account remove-member <account> <username> --yes"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountRemoveMember(cmd.Context(), *opts, args[0], args[1], removeYes)
		},
	}
	removeMemberCmd.Flags().BoolVar(&removeYes, "yes", removeYes, "confirm removing the member")
	accountCmd.AddCommand(createOrgCmd, membersCmd, setMemberCmd, removeMemberCmd)
	return accountCmd
}

func (r Runner) runAccountCreateOrg(ctx context.Context, opts commandOptions, slug string, owners []string) error {
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).CreateOrganization(authCtx, &corev1.CreateOrganizationRequest{
		Slug:           strings.TrimSpace(slug),
		OwnerUsernames: owners,
	})
	if err != nil {
		return accountError(err)
	}
	out := accountMembersOutput{Account: resp.Account, Kind: "organization", Members: accountMembersFromProto(resp.Members)}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "created organization %s\n", resp.Account)
		r.printAccountMembers(out.Members)
	}
	return nil
}

func (r Runner) runAccountMembers(ctx context.Context, opts commandOptions, account string) error {
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).ListAccountMembers(authCtx, &corev1.ListAccountMembersRequest{Account: strings.TrimSpace(account)})
	if err != nil {
		return accountError(err)
	}
	out := accountMembersOutput{Account: resp.Account, Kind: resp.Kind, Members: accountMembersFromProto(resp.Members)}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	r.printAccountMembers(out.Members)
	return nil
}

func (r Runner) runAccountSetMember(ctx context.Context, opts commandOptions, account, username, role string) error {
	if strings.TrimSpace(role) == "" {
		return userError("role_required", "--role is required", "Pass --role owner, admin, writer, member or reader.")
	}
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).SetAccountMember(authCtx, &corev1.SetAccountMemberRequest{
		Account:  strings.TrimSpace(account),
		Username: strings.TrimSpace(username),
		Role:     strings.TrimSpace(role),
	})
	if err != nil {
		return accountError(err)
	}
	member := accountMemberOutput{Username: resp.Member.GetUsername(), SubjectID: resp.Member.GetSubjectId(), Role: resp.Member.GetRole()}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, member)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "%s is now %s of %s\n", member.Username, member.Role, strings.TrimSpace(account))
	}
	return nil
}

func (r Runner) runAccountRemoveMember(ctx context.Context, opts commandOptions, account, username string, yes bool) error {
	if !yes {
		return userError("confirmation_required", "remove-member requires --yes", "Run gs account remove-member <account> <username> --yes.")
	}
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := corev1.NewAuthServiceClient(conn).RemoveAccountMember(authCtx, &corev1.RemoveAccountMemberRequest{
		Account:  strings.TrimSpace(account),
		Username: strings.TrimSpace(username),
	}); err != nil {
		return accountError(err)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"account": strings.TrimSpace(account), "username": strings.TrimSpace(username), "removed": true})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "removed %s from %s\n", strings.TrimSpace(username), strings.TrimSpace(account))
	}
	return nil
}

func (r Runner) printAccountMembers(members []accountMemberOutput) {
	for _, member := range members {
		name := member.Username
		if name == "" {
			name = member.SubjectID
		}
		fmt.Fprintf(r.Stdout, "%s\t%s\n", name, member.Role)
	}
}

func accountMembersFromProto(members []*corev1.AccountMember) []accountMemberOutput {
	out := make([]accountMemberOutput, 0, len(members))
	for _, member := range members {
		out = append(out, accountMemberOutput{Username: member.Username, SubjectID: member.SubjectId, Role: member.Role})
	}
	return out
}

func accountError(err error) error {
	message := grpcstatus.Convert(err).Message()
	switch grpcstatus.Code(err) {
	case codes.PermissionDenied:
		return userError("permission_denied", message, "Organizations are created by server operators; members are managed by owners and admins.")
	case codes.NotFound:
		return userError("not_found", message, "Check the account and username, and that you are a member.")
	case codes.AlreadyExists:
		return userError("already_exists", message, "Pick another name or manage the existing account with gs account set-member.")
	case codes.InvalidArgument:
		return userError("invalid_argument", message, "")
	case codes.FailedPrecondition:
		return userError("failed_precondition", message, "")
	}
	return err
}
