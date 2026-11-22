package cli

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (r Runner) runAccountCIRunners(ctx context.Context, opts commandOptions, account string) error {
	var out accountCIRunnersOutput
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		if account, err = s.ResolveAccount(account); err != nil {
			return err
		}
		resp, err := s.Slice().GetAccountCIDaemons(s.Ctx(), &corev1.GetAccountCIDaemonsRequest{Account: account})
		if err != nil {
			return accountError(err)
		}
		out = accountCIRunnersOutput{
			Account:     resp.GetAccount(),
			DaemonIDs:   append([]string(nil), resp.GetDaemonIds()...),
			DaemonNames: append([]string(nil), resp.GetDaemonNames()...),
		}
		return nil
	}); err != nil {
		return err
	}
	return writeSchemaOutput(r, opts, out, func() error {
		return r.printAccountCIRunners(out)
	})
}

func (r Runner) runAccountSetCIDaemon(ctx context.Context, opts commandOptions, account, daemonID string, add, remove bool) error {
	var out accountCIRunnersOutput
	if err := r.withSession(ctx, func(s *Session) error {
		resp, err := s.Slice().SetAccountCIDaemon(s.Ctx(), &corev1.SetAccountCIDaemonRequest{
			Account:  strings.TrimSpace(account),
			DaemonId: strings.TrimSpace(daemonID),
			Add:      add,
			Remove:   remove,
		})
		if err != nil {
			return accountError(err)
		}
		out = accountCIRunnersOutput{
			Account:     resp.GetAccount(),
			DaemonIDs:   append([]string(nil), resp.GetDaemonIds()...),
			DaemonNames: append([]string(nil), resp.GetDaemonNames()...),
		}
		return nil
	}); err != nil {
		return err
	}
	return writeSchemaOutput(r, opts, out, func() error {
		return r.printAccountCIRunners(out)
	})
}

func (r Runner) runAccountInvite(ctx context.Context, opts commandOptions, org, username, role string) error {
	normalizedRole := strings.ToLower(strings.TrimSpace(role))
	if model.AccountRoleRank(normalizedRole) >= len(model.AccountRoles) {
		return userError("invalid_role", fmt.Sprintf("role must be one of %s", strings.Join(model.AccountRoles, ", ")), "Pass --role owner, admin, writer, member, contributor or reader.")
	}
	var out invitationOutput
	if err := r.withSession(ctx, func(s *Session) error {
		resp, err := s.Auth().InviteAccountMember(s.Ctx(), &corev1.InviteAccountMemberRequest{
			Account:  strings.TrimSpace(org),
			Username: strings.TrimPrefix(strings.TrimSpace(username), "@"),
			Role:     normalizedRole,
		})
		if err != nil {
			return accountError(err)
		}
		out = invitationFromProto(resp.Invitation)
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "invited %s to %s as %s; they join when they run gs account accept %s or accept in the web app\n", out.Username, out.Account, out.Role, out.Account)
	}
	return nil
}

func (r Runner) runAccountInvitations(ctx context.Context, opts commandOptions, org string) error {
	var invitations []*corev1.AccountInvitation
	if err := r.withSession(ctx, func(s *Session) error {
		if strings.TrimSpace(org) == "" {
			resp, err := s.Auth().ListMyInvitations(s.Ctx(), &corev1.ListMyInvitationsRequest{})
			if err != nil {
				return accountError(err)
			}
			invitations = resp.Invitations
			return nil
		}
		resp, err := s.Auth().ListAccountInvitations(s.Ctx(), &corev1.ListAccountInvitationsRequest{Account: strings.TrimSpace(org)})
		if err != nil {
			return accountError(err)
		}
		invitations = resp.Invitations
		return nil
	}); err != nil {
		return err
	}
	out := make([]invitationOutput, 0, len(invitations))
	for _, invitation := range invitations {
		out = append(out, invitationFromProto(invitation))
	}
	if opts.JSONOutput() {
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
	var resp *corev1.RespondToInvitationResponse
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		resp, err = s.Auth().RespondToInvitation(s.Ctx(), &corev1.RespondToInvitationRequest{Account: strings.TrimSpace(org), Accept: accept})
		return accountError(err)
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
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
	if err := r.withSession(ctx, func(s *Session) error {
		_, err := s.Auth().CancelAccountInvitation(s.Ctx(), &corev1.CancelAccountInvitationRequest{
			Account:  strings.TrimSpace(org),
			Username: strings.TrimPrefix(strings.TrimSpace(username), "@"),
		})
		return accountError(err)
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
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
	var cfg UserConfig
	if err := r.withSession(ctx, func(s *Session) error {
		cfg = s.Config()
		if !clear {
			status, err := s.Auth().GetAuthStatus(s.Ctx(), &corev1.GetAuthStatusRequest{})
			if err != nil {
				return err
			}
			if !containsFold(status.Accounts, account) {
				return userError("not_found", fmt.Sprintf("you do not belong to %s", account), "Run gs auth status to see your accounts.")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	cfg.ActiveAccount = account
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	if opts.JSONOutput() {
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
	var active string
	source := "active"
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		active, err = s.ActiveAccount()
		if err != nil {
			return err
		}
		if active == "" {
			if active, err = s.PersonalAccountSlug(); err != nil {
				return err
			}
			source = "personal"
		}
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
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

func (r Runner) runAccountProfile(ctx context.Context, opts commandOptions, account string) error {
	var out profileOutput
	if err := r.withSession(ctx, func(s *Session) error {
		resp, err := s.Auth().GetAccountProfile(s.Ctx(), &corev1.GetAccountProfileRequest{Account: strings.TrimSpace(account)})
		if err != nil {
			return accountError(err)
		}
		out = profileFromProto(resp)
		return nil
	}); err != nil {
		return err
	}
	return r.writeProfile(opts, out)
}

func (r Runner) runAccountSetProfile(ctx context.Context, opts commandOptions, account string, name, description, website *string) error {
	if name == nil && description == nil && website == nil {
		return userError("nothing_to_change", "nothing to change", "Pass --name, --description or --website.")
	}
	var out profileOutput
	if err := r.withSession(ctx, func(s *Session) error {
		current, err := s.Auth().GetAccountProfile(s.Ctx(), &corev1.GetAccountProfileRequest{Account: strings.TrimSpace(account)})
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
		updated, err := s.Auth().UpdateAccountProfile(s.Ctx(), req)
		if err != nil {
			return accountError(err)
		}
		out = profileFromProto(updated)
		return nil
	}); err != nil {
		return err
	}
	return r.writeProfile(opts, out)
}

func (r Runner) runAccountCreateOrg(ctx context.Context, opts commandOptions, slug string, owners []string) error {
	var out accountMembersOutput
	if err := r.withSession(ctx, func(s *Session) error {
		resp, err := s.Auth().CreateOrganization(s.Ctx(), &corev1.CreateOrganizationRequest{
			Slug:           strings.TrimSpace(slug),
			OwnerUsernames: owners,
		})
		if err != nil {
			return accountError(err)
		}
		out = accountMembersOutput{Account: resp.Account, Kind: "organization", Members: accountMembersFromProto(resp.Members)}
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "created organization %s\n", out.Account)
		r.printAccountMembers(out.Members)
	}
	return nil
}
