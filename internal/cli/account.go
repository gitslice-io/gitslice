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
		Short: "Manage accounts: organizations, members, invitations, profiles and the active account",
		RunE:  requireSubcommand("account"),
	}
	var owners []string
	createOrgCmd := &cobra.Command{
		Use:   "create-org <slug>",
		Short: "Create an organization account; you become its owner",
		Args:  exactArgs(1, "gs account create-org <slug> [--owner <username>]..."),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountCreateOrg(cmd.Context(), *opts, args[0], owners)
		},
	}
	createOrgCmd.Flags().StringArrayVar(&owners, "owner", nil, "username of an initial owner; repeat for several (operators only; default: you)")
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
		Short: "Change a member's role (operators may also add members directly)",
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
	inviteRole := "writer"
	inviteCmd := &cobra.Command{
		Use:   "invite <org> <username>",
		Short: "Invite a user to an organization; they join when they accept",
		Args:  exactArgs(2, "gs account invite <org> <username> [--role writer]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountInvite(cmd.Context(), *opts, args[0], args[1], inviteRole)
		},
	}
	inviteCmd.Flags().StringVar(&inviteRole, "role", inviteRole, "role: owner, admin, writer, member or reader")
	invitationsCmd := &cobra.Command{
		Use:   "invitations [<org>]",
		Short: "List your pending invitations, or an organization's",
		Args:  maxArgs(1, "gs account invitations [<org>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := ""
			if len(args) == 1 {
				org = args[0]
			}
			return r.runAccountInvitations(cmd.Context(), *opts, org)
		},
	}
	acceptCmd := &cobra.Command{
		Use:   "accept <org>",
		Short: "Accept your invitation to an organization",
		Args:  exactArgs(1, "gs account accept <org>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountRespond(cmd.Context(), *opts, args[0], true)
		},
	}
	declineCmd := &cobra.Command{
		Use:   "decline <org>",
		Short: "Decline your invitation to an organization",
		Args:  exactArgs(1, "gs account decline <org>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountRespond(cmd.Context(), *opts, args[0], false)
		},
	}
	cancelInviteCmd := &cobra.Command{
		Use:   "cancel-invite <org> <username>",
		Short: "Withdraw a pending invitation",
		Args:  exactArgs(2, "gs account cancel-invite <org> <username>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountCancelInvite(cmd.Context(), *opts, args[0], args[1])
		},
	}
	clearActive := false
	useCmd := &cobra.Command{
		Use:   "use [<account>]",
		Short: "Make an account you belong to the default for commands (like gs slice list)",
		Args:  maxArgs(1, "gs account use <account> | gs account use --clear"),
		RunE: func(cmd *cobra.Command, args []string) error {
			account := ""
			if len(args) == 1 {
				account = args[0]
			}
			return r.runAccountUse(cmd.Context(), *opts, account, clearActive)
		},
	}
	useCmd.Flags().BoolVar(&clearActive, "clear", false, "go back to your personal account")
	currentCmd := &cobra.Command{
		Use:   "current",
		Short: "Show the account commands default to",
		Args:  exactArgs(0, "gs account current"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountCurrent(cmd.Context(), *opts)
		},
	}
	profileCmd := &cobra.Command{
		Use:   "profile <account>",
		Short: "Show an account's public profile",
		Args:  exactArgs(1, "gs account profile <account>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountProfile(cmd.Context(), *opts, args[0])
		},
	}
	var profileName, profileDescription, profileWebsite string
	setProfileCmd := &cobra.Command{
		Use:   "set-profile <account>",
		Short: "Change an account's display name, description or website",
		Args:  exactArgs(1, "gs account set-profile <account> [--name ...] [--description ...] [--website ...]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name, description, website *string
			if cmd.Flags().Changed("name") {
				name = &profileName
			}
			if cmd.Flags().Changed("description") {
				description = &profileDescription
			}
			if cmd.Flags().Changed("website") {
				website = &profileWebsite
			}
			return r.runAccountSetProfile(cmd.Context(), *opts, args[0], name, description, website)
		},
	}
	setProfileCmd.Flags().StringVar(&profileName, "name", "", "display name (at most 64 characters; empty clears it)")
	setProfileCmd.Flags().StringVar(&profileDescription, "description", "", "description (at most 280 characters)")
	setProfileCmd.Flags().StringVar(&profileWebsite, "website", "", "an http or https URL")
	accountCmd.AddCommand(createOrgCmd, membersCmd, setMemberCmd, removeMemberCmd, inviteCmd, invitationsCmd, acceptCmd, declineCmd, cancelInviteCmd, useCmd, currentCmd, profileCmd, setProfileCmd)
	return accountCmd
}

type invitationOutput struct {
	Account   string `json:"account"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	InvitedBy string `json:"invited_by,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

func invitationFromProto(i *corev1.AccountInvitation) invitationOutput {
	return invitationOutput{Account: i.GetAccount(), Username: i.GetUsername(), Role: i.GetRole(), InvitedBy: i.GetInvitedBy(), CreatedAt: i.GetCreatedAt()}
}

func (r Runner) runAccountInvite(ctx context.Context, opts commandOptions, org, username, role string) error {
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).InviteAccountMember(authCtx, &corev1.InviteAccountMemberRequest{
		Account:  strings.TrimSpace(org),
		Username: strings.TrimPrefix(strings.TrimSpace(username), "@"),
		Role:     strings.TrimSpace(role),
	})
	if err != nil {
		return accountError(err)
	}
	out := invitationFromProto(resp.Invitation)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "invited %s to %s as %s; they join when they run gs account accept %s or accept in the web app\n", out.Username, out.Account, out.Role, out.Account)
	}
	return nil
}

func (r Runner) runAccountInvitations(ctx context.Context, opts commandOptions, org string) error {
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewAuthServiceClient(conn)
	var invitations []*corev1.AccountInvitation
	if strings.TrimSpace(org) == "" {
		resp, err := client.ListMyInvitations(authCtx, &corev1.ListMyInvitationsRequest{})
		if err != nil {
			return accountError(err)
		}
		invitations = resp.Invitations
	} else {
		resp, err := client.ListAccountInvitations(authCtx, &corev1.ListAccountInvitationsRequest{Account: strings.TrimSpace(org)})
		if err != nil {
			return accountError(err)
		}
		invitations = resp.Invitations
	}
	out := make([]invitationOutput, 0, len(invitations))
	for _, invitation := range invitations {
		out = append(out, invitationFromProto(invitation))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"invitations": out})
	}
	if opts.Quiet {
		return nil
	}
	if len(out) == 0 {
		fmt.Fprintln(r.Stdout, "no pending invitations")
		return nil
	}
	for _, invitation := range out {
		if strings.TrimSpace(org) == "" {
			fmt.Fprintf(r.Stdout, "%s\t%s\tinvited by %s\n", invitation.Account, invitation.Role, invitation.InvitedBy)
		} else {
			fmt.Fprintf(r.Stdout, "%s\t%s\tinvited by %s\n", invitation.Username, invitation.Role, invitation.InvitedBy)
		}
	}
	return nil
}

func (r Runner) runAccountRespond(ctx context.Context, opts commandOptions, org string, accept bool) error {
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).RespondToInvitation(authCtx, &corev1.RespondToInvitationRequest{Account: strings.TrimSpace(org), Accept: accept})
	if err != nil {
		return accountError(err)
	}
	if opts.jsonOutput() {
		out := map[string]any{"account": strings.TrimSpace(org), "accepted": accept}
		if m := resp.GetMembership(); m != nil {
			out["role"] = m.GetRole()
		}
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		if accept {
			fmt.Fprintf(r.Stdout, "joined %s as %s\n", strings.TrimSpace(org), resp.GetMembership().GetRole())
		} else {
			fmt.Fprintf(r.Stdout, "declined the invitation to %s\n", strings.TrimSpace(org))
		}
	}
	return nil
}

func (r Runner) runAccountCancelInvite(ctx context.Context, opts commandOptions, org, username string) error {
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := corev1.NewAuthServiceClient(conn).CancelAccountInvitation(authCtx, &corev1.CancelAccountInvitationRequest{
		Account:  strings.TrimSpace(org),
		Username: strings.TrimPrefix(strings.TrimSpace(username), "@"),
	}); err != nil {
		return accountError(err)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"account": strings.TrimSpace(org), "username": strings.TrimSpace(username), "cancelled": true})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "cancelled the invitation of %s to %s\n", strings.TrimSpace(username), strings.TrimSpace(org))
	}
	return nil
}

func (r Runner) runAccountUse(ctx context.Context, opts commandOptions, account string, clear bool) error {
	account = strings.TrimSpace(account)
	if clear == (account != "") {
		return userError("invalid_arguments", "pass an account or --clear", "Run gs account use <account>, or gs account use --clear.")
	}
	cfg, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if !clear {
		status, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(authCtx, &corev1.GetAuthStatusRequest{})
		if err != nil {
			return err
		}
		if !containsFold(status.Accounts, account) {
			return userError("not_a_member", fmt.Sprintf("you do not belong to %s", account), "Run gs auth status to see your accounts.")
		}
	}
	cfg.ActiveAccount = account
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"active_account": account})
	}
	if !opts.Quiet {
		if clear {
			fmt.Fprintln(r.Stdout, "commands default to your personal account again")
		} else {
			fmt.Fprintf(r.Stdout, "commands now default to %s\n", account)
		}
	}
	return nil
}

func (r Runner) runAccountCurrent(ctx context.Context, opts commandOptions) error {
	cfg, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	active, err := r.activeAccount(authCtx, cfg, conn)
	if err != nil {
		return err
	}
	source := "active"
	if active == "" {
		if active, err = r.personalAccountSlug(authCtx, conn); err != nil {
			return err
		}
		source = "personal"
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"account": active, "source": source})
	}
	if !opts.Quiet {
		if source == "personal" {
			fmt.Fprintf(r.Stdout, "%s (your personal account, the default)\n", active)
		} else {
			fmt.Fprintln(r.Stdout, active)
		}
	}
	return nil
}

type profileOutput struct {
	Account     string `json:"account"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Website     string `json:"website,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

func profileFromProto(p *corev1.AccountProfile) profileOutput {
	return profileOutput{Account: p.GetAccount(), Kind: p.GetKind(), DisplayName: p.GetDisplayName(), Description: p.GetDescription(), Website: p.GetWebsite(), CreatedAt: p.GetCreatedAt()}
}

func (r Runner) writeProfile(opts commandOptions, p profileOutput) error {
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, p)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "%s (%s)\n", p.Account, p.Kind)
	for _, field := range [][2]string{{"name", p.DisplayName}, {"description", p.Description}, {"website", p.Website}, {"created", p.CreatedAt}} {
		if field[1] != "" {
			fmt.Fprintf(r.Stdout, "%s: %s\n", field[0], field[1])
		}
	}
	return nil
}

func (r Runner) runAccountProfile(ctx context.Context, opts commandOptions, account string) error {
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).GetAccountProfile(authCtx, &corev1.GetAccountProfileRequest{Account: strings.TrimSpace(account)})
	if err != nil {
		return accountError(err)
	}
	return r.writeProfile(opts, profileFromProto(resp))
}

func (r Runner) runAccountSetProfile(ctx context.Context, opts commandOptions, account string, name, description, website *string) error {
	if name == nil && description == nil && website == nil {
		return userError("nothing_to_change", "nothing to change", "Pass --name, --description or --website.")
	}
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewAuthServiceClient(conn)
	current, err := client.GetAccountProfile(authCtx, &corev1.GetAccountProfileRequest{Account: strings.TrimSpace(account)})
	if err != nil {
		return accountError(err)
	}
	req := &corev1.UpdateAccountProfileRequest{
		Account:     strings.TrimSpace(account),
		DisplayName: current.GetDisplayName(),
		Description: current.GetDescription(),
		Website:     current.GetWebsite(),
	}
	if name != nil {
		req.DisplayName = *name
	}
	if description != nil {
		req.Description = *description
	}
	if website != nil {
		req.Website = *website
	}
	updated, err := client.UpdateAccountProfile(authCtx, req)
	if err != nil {
		return accountError(err)
	}
	return r.writeProfile(opts, profileFromProto(updated))
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
		return userError("permission_denied", message, "Members, invitations and organization profiles are managed by owners and admins.")
	case codes.NotFound:
		return userError("not_found", message, "Check the account and username, and that you are a member.")
	case codes.AlreadyExists:
		return userError("already_exists", message, "Pick another name, or change an existing member's role with gs account set-member.")
	case codes.InvalidArgument:
		return userError("invalid_argument", message, "")
	case codes.FailedPrecondition:
		hint := ""
		if strings.Contains(message, "invite them") {
			hint = "Run gs account invite <org> <username> --role <role>; they join when they accept."
		}
		return userError("failed_precondition", message, hint)
	case codes.ResourceExhausted:
		return userError("limit_reached", message, "")
	}
	return err
}
