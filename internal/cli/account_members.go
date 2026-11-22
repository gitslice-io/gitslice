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

type accountMemberOutput struct {
	Username     string `json:"username"`
	SubjectID    string `json:"subject_id"`
	Role         string `json:"role"`
	DisplayName  string `json:"display_name,omitempty"`
	Kind         string `json:"kind,omitempty"`
	You          bool   `json:"you,omitempty"`
	ClaimedAgent bool   `json:"claimed_agent,omitempty"`
}

type accountMembersOutput struct {
	Account string                `json:"account"`
	Kind    string                `json:"kind,omitempty"`
	Members []accountMemberOutput `json:"members"`
}

func (r Runner) runAccountMembers(ctx context.Context, opts commandOptions, account string) error {
	var out accountMembersOutput
	if err := r.withSession(ctx, func(s *Session) error {
		resp, err := s.Auth().ListAccountMembers(s.Ctx(), &corev1.ListAccountMembersRequest{Account: strings.TrimSpace(account)})
		if err != nil {
			if grpcstatus.Code(err) == codes.NotFound {
				return s.accountNotFoundError(strings.TrimSpace(account), err)
			}
			return accountError(err)
		}
		members := accountMembersFromProto(resp.Members)
		markAccountMembers(members, resp.Account, s.Config().SubjectID)
		out = accountMembersOutput{Account: resp.Account, Kind: resp.Kind, Members: members}
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, out)
	}
	r.printAccountMembers(out.Members)
	return nil
}

// accountNotFoundError answers gs account members for an account the caller
// does not belong to, surfacing any slices of that account shared with them.
func (s *Session) accountNotFoundError(account string, err error) error {
	message := grpcstatus.Convert(err).Message()
	shared, listErr := listSharedSlices(s.Ctx(), s.Slice())
	if listErr == nil {
		var refs, labels []string
		for _, sl := range shared {
			if strings.HasPrefix(sl.Ref, account+":") {
				refs = append(refs, sl.Ref)
				labels = append(labels, fmt.Sprintf("%s (%s)", sl.Ref, sl.Role))
			}
		}
		if len(refs) > 0 {
			return userError("not_found",
				"you are not a member of account "+account+", but it shares "+strings.Join(labels, ", ")+" with you",
				"gs slice members "+refs[0]+" shows who else has it; gs slice list --shared lists every slice shared with you.")
		}
	}
	return userError("not_found", message, "Check the account and username, and that you are a member. Slices shared with you from accounts you are not in: gs slice list --shared.")
}

func markAccountMembers(members []accountMemberOutput, account, subjectID string) {
	agentAccount := false
	for _, member := range members {
		if member.Kind == "agent" && member.Username == account {
			agentAccount = true
		}
	}
	for i := range members {
		members[i].You = subjectID != "" && members[i].SubjectID == subjectID
		members[i].ClaimedAgent = agentAccount && members[i].Kind == "person"
	}
}

func (r Runner) runAccountSetMember(ctx context.Context, opts commandOptions, account, username, role string) error {
	normalizedRole := strings.ToLower(strings.TrimSpace(role))
	if normalizedRole == "" {
		return userError("role_required", "--role is required", "Pass --role owner, admin, writer, member, contributor or reader.")
	}
	if model.AccountRoleRank(normalizedRole) >= len(model.AccountRoles) {
		return userError("invalid_role", fmt.Sprintf("role must be one of %s", strings.Join(model.AccountRoles, ", ")), "Pass --role owner, admin, writer, member, contributor or reader.")
	}
	var member accountMemberOutput
	if err := r.withSession(ctx, func(s *Session) error {
		resp, err := s.Auth().SetAccountMember(s.Ctx(), &corev1.SetAccountMemberRequest{
			Account:  strings.TrimSpace(account),
			Username: strings.TrimSpace(username),
			Role:     normalizedRole,
		})
		if err != nil {
			return accountError(err)
		}
		member = accountMemberOutput{Username: resp.Member.GetUsername(), SubjectID: resp.Member.GetSubjectId(), Role: resp.Member.GetRole()}
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
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
	if err := r.withSession(ctx, func(s *Session) error {
		_, err := s.Auth().RemoveAccountMember(s.Ctx(), &corev1.RemoveAccountMemberRequest{
			Account:  strings.TrimSpace(account),
			Username: strings.TrimSpace(username),
		})
		return accountError(err)
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
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
		var notes []string
		if member.DisplayName != "" {
			notes = append(notes, member.DisplayName)
		}
		if member.Kind != "" {
			notes = append(notes, member.Kind)
		}
		if member.ClaimedAgent {
			notes = append(notes, "claimed this agent")
		}
		if member.You {
			notes = append(notes, "you")
		}
		line := name + "\t" + member.Role
		if len(notes) > 0 {
			line += "\t(" + strings.Join(notes, ", ") + ")"
		}
		fmt.Fprintln(r.Stdout, line)
	}
}

func accountMembersFromProto(members []*corev1.AccountMember) []accountMemberOutput {
	out := make([]accountMemberOutput, 0, len(members))
	for _, member := range members {
		out = append(out, accountMemberOutput{
			Username:    member.GetUsername(),
			SubjectID:   member.GetSubjectId(),
			Role:        member.GetRole(),
			DisplayName: member.GetDisplayName(),
			Kind:        member.GetKind(),
		})
	}
	return out
}
