package account

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/cli/clibase"
	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/workspace"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func runCIRunners(app clibase.App, ctx context.Context, opts clibase.Options, account string) error {
	var out ciRunnersOutput
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		var err error
		if account, err = s.ResolveAccount(account); err != nil {
			return err
		}
		resp, err := s.Slice().GetAccountCIDaemons(s.Ctx(), &corev1.GetAccountCIDaemonsRequest{Account: account})
		if err != nil {
			return accountError(err)
		}
		out = ciRunnersOutput{
			Account:     resp.GetAccount(),
			DaemonIDs:   append([]string(nil), resp.GetDaemonIds()...),
			DaemonNames: append([]string(nil), resp.GetDaemonNames()...),
		}
		return nil
	}); err != nil {
		return err
	}
	return clibase.WriteSchemaOutput(app.Stdout(), opts, out, func() error {
		return printCIRunners(app.Stdout(), out)
	})
}

func runSetCIDaemon(app clibase.App, ctx context.Context, opts clibase.Options, account, daemonID string, add, remove bool) error {
	var out ciRunnersOutput
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		resp, err := s.Slice().SetAccountCIDaemon(s.Ctx(), &corev1.SetAccountCIDaemonRequest{
			Account:  strings.TrimSpace(account),
			DaemonId: strings.TrimSpace(daemonID),
			Add:      add,
			Remove:   remove,
		})
		if err != nil {
			return accountError(err)
		}
		out = ciRunnersOutput{
			Account:     resp.GetAccount(),
			DaemonIDs:   append([]string(nil), resp.GetDaemonIds()...),
			DaemonNames: append([]string(nil), resp.GetDaemonNames()...),
		}
		return nil
	}); err != nil {
		return err
	}
	return clibase.WriteSchemaOutput(app.Stdout(), opts, out, func() error {
		return printCIRunners(app.Stdout(), out)
	})
}

func runInvite(app clibase.App, ctx context.Context, opts clibase.Options, org, username, role string) error {
	normalizedRole := strings.ToLower(strings.TrimSpace(role))
	if model.AccountRoleRank(normalizedRole) >= len(model.AccountRoles) {
		return clibase.UserError("invalid_role", fmt.Sprintf("role must be one of %s", strings.Join(model.AccountRoles, ", ")), "Pass --role owner, admin, writer, member, contributor or reader.")
	}
	var out invitationOutput
	if err := app.WithSession(ctx, func(s clibase.Session) error {
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
		return app.WriteJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(app.Stdout(), "invited %s to %s as %s; they join when they run gs account accept %s or accept in the web app\n", out.Username, out.Account, out.Role, out.Account)
	}
	return nil
}

func runInvitations(app clibase.App, ctx context.Context, opts clibase.Options, org string) error {
	var invitations []*corev1.AccountInvitation
	if err := app.WithSession(ctx, func(s clibase.Session) error {
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
		return app.WriteJSONOutput(opts, map[string]any{"invitations": out})
	}
	if opts.Quiet {
		return nil
	}
	if len(out) == 0 {
		fmt.Fprintln(app.Stdout(), "no pending invitations")
		return nil
	}
	for _, invitation := range out {
		if strings.TrimSpace(org) == "" {
			fmt.Fprintf(app.Stdout(), "%s\t%s\tinvited by %s\n", invitation.Account, invitation.Role, invitation.InvitedBy)
		} else {
			fmt.Fprintf(app.Stdout(), "%s\t%s\tinvited by %s\n", invitation.Username, invitation.Role, invitation.InvitedBy)
		}
	}
	return nil
}

func runRespond(app clibase.App, ctx context.Context, opts clibase.Options, org string, accept bool) error {
	var resp *corev1.RespondToInvitationResponse
	if err := app.WithSession(ctx, func(s clibase.Session) error {
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
		return app.WriteJSONOutput(opts, out)
	}
	if !opts.Quiet {
		if accept {
			fmt.Fprintf(app.Stdout(), "joined %s as %s\n", strings.TrimSpace(org), resp.GetMembership().GetRole())
		} else {
			fmt.Fprintf(app.Stdout(), "declined the invitation to %s\n", strings.TrimSpace(org))
		}
	}
	return nil
}

func runCancelInvite(app clibase.App, ctx context.Context, opts clibase.Options, org, username string) error {
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		_, err := s.Auth().CancelAccountInvitation(s.Ctx(), &corev1.CancelAccountInvitationRequest{
			Account:  strings.TrimSpace(org),
			Username: strings.TrimPrefix(strings.TrimSpace(username), "@"),
		})
		return accountError(err)
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, map[string]any{"account": strings.TrimSpace(org), "username": strings.TrimSpace(username), "cancelled": true})
	}
	if !opts.Quiet {
		fmt.Fprintf(app.Stdout(), "cancelled the invitation of %s to %s\n", strings.TrimSpace(username), strings.TrimSpace(org))
	}
	return nil
}

func runUse(app clibase.App, ctx context.Context, opts clibase.Options, account string, clear bool) error {
	account = strings.TrimSpace(account)
	if clear == (account != "") {
		return clibase.UserError("invalid_arguments", "pass an account or --clear", "Run gs account use <account>, or gs account use --clear.")
	}
	var cfg workspace.UserConfig
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		cfg = s.Config()
		if !clear {
			status, err := s.Auth().GetAuthStatus(s.Ctx(), &corev1.GetAuthStatusRequest{})
			if err != nil {
				return err
			}
			if !clibase.ContainsFold(status.Accounts, account) {
				return clibase.UserError("not_found", fmt.Sprintf("you do not belong to %s", account), "Run gs auth status to see your accounts.")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	cfg.ActiveAccount = account
	if err := app.WriteUserConfig(cfg); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, map[string]any{"active_account": account})
	}
	if !opts.Quiet {
		if clear {
			fmt.Fprintln(app.Stdout(), "commands default to your personal account again")
		} else {
			fmt.Fprintf(app.Stdout(), "commands now default to %s\n", account)
		}
	}
	return nil
}

func runCurrent(app clibase.App, ctx context.Context, opts clibase.Options) error {
	var active string
	source := "active"
	if err := app.WithSession(ctx, func(s clibase.Session) error {
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
		return app.WriteJSONOutput(opts, map[string]any{"account": active, "source": source})
	}
	if !opts.Quiet {
		if source == "personal" {
			fmt.Fprintf(app.Stdout(), "%s (your personal account, the default)\n", active)
		} else {
			fmt.Fprintln(app.Stdout(), active)
		}
	}
	return nil
}

func runProfile(app clibase.App, ctx context.Context, opts clibase.Options, account string) error {
	var out profileOutput
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		resp, err := s.Auth().GetAccountProfile(s.Ctx(), &corev1.GetAccountProfileRequest{Account: strings.TrimSpace(account)})
		if err != nil {
			return accountError(err)
		}
		out = profileFromProto(resp)
		return nil
	}); err != nil {
		return err
	}
	return writeProfile(app, opts, out)
}

func runSetProfile(app clibase.App, ctx context.Context, opts clibase.Options, account string, name, description, website *string) error {
	if name == nil && description == nil && website == nil {
		return clibase.UserError("nothing_to_change", "nothing to change", "Pass --name, --description or --website.")
	}
	var out profileOutput
	if err := app.WithSession(ctx, func(s clibase.Session) error {
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
	return writeProfile(app, opts, out)
}

func runCreateOrg(app clibase.App, ctx context.Context, opts clibase.Options, slug string, owners []string) error {
	var out membersOutput
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		resp, err := s.Auth().CreateOrganization(s.Ctx(), &corev1.CreateOrganizationRequest{
			Slug:           strings.TrimSpace(slug),
			OwnerUsernames: owners,
		})
		if err != nil {
			return accountError(err)
		}
		out = membersOutput{Account: resp.Account, Kind: "organization", Members: membersFromProto(resp.Members)}
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(app.Stdout(), "created organization %s\n", out.Account)
		printMembers(app.Stdout(), out.Members)
	}
	return nil
}
