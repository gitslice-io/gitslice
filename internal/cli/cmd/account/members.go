package account

import (
	"context"
	"fmt"
	"io"
	"strings"

	"gitslice.io/gitslice/internal/cli/clibase"
	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type memberOutput struct {
	Username     string `json:"username"`
	SubjectID    string `json:"subject_id"`
	Role         string `json:"role"`
	DisplayName  string `json:"display_name,omitempty"`
	Kind         string `json:"kind,omitempty"`
	You          bool   `json:"you,omitempty"`
	ClaimedAgent bool   `json:"claimed_agent,omitempty"`
}

type membersOutput struct {
	Account string         `json:"account"`
	Kind    string         `json:"kind,omitempty"`
	Members []memberOutput `json:"members"`
}

func runMembers(app clibase.App, ctx context.Context, opts clibase.Options, account string) error {
	var out membersOutput
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		resp, err := s.Auth().ListAccountMembers(s.Ctx(), &corev1.ListAccountMembersRequest{Account: strings.TrimSpace(account)})
		if err != nil {
			if grpcstatus.Code(err) == codes.NotFound {
				return accountNotFoundError(s, strings.TrimSpace(account), err)
			}
			return accountError(err)
		}
		members := membersFromProto(resp.Members)
		markMembers(members, resp.Account, s.Config().SubjectID)
		out = membersOutput{Account: resp.Account, Kind: resp.Kind, Members: members}
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, out)
	}
	printMembers(app.Stdout(), out.Members)
	return nil
}

func accountNotFoundError(s clibase.Session, account string, err error) error {
	message := grpcstatus.Convert(err).Message()
	res, listErr := s.Slice().ListSharedSlices(s.Ctx(), &corev1.ListSharedSlicesRequest{})
	if listErr == nil {
		var refs, labels []string
		for _, sl := range res.GetSlices() {
			ref := sl.GetSlice().GetRef().GetAccount() + ":" + sl.GetSlice().GetRef().GetSlice()
			if strings.HasPrefix(ref, account+":") {
				refs = append(refs, ref)
				labels = append(labels, fmt.Sprintf("%s (%s)", ref, sl.GetRole()))
			}
		}
		if len(refs) > 0 {
			return clibase.UserError("not_found",
				"you are not a member of account "+account+", but it shares "+strings.Join(labels, ", ")+" with you",
				"gs slice members "+refs[0]+" shows who else has it; gs slice list --shared lists every slice shared with you.")
		}
	}
	return clibase.UserError("not_found", message, "Check the account and username, and that you are a member. Slices shared with you from accounts you are not in: gs slice list --shared.")
}

func markMembers(members []memberOutput, account, subjectID string) {
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

func runSetMember(app clibase.App, ctx context.Context, opts clibase.Options, account, username, role string) error {
	normalizedRole := strings.ToLower(strings.TrimSpace(role))
	if normalizedRole == "" {
		return clibase.UserError("role_required", "--role is required", "Pass --role owner, admin, writer, member, contributor or reader.")
	}
	if model.AccountRoleRank(normalizedRole) >= len(model.AccountRoles) {
		return clibase.UserError("invalid_role", fmt.Sprintf("role must be one of %s", strings.Join(model.AccountRoles, ", ")), "Pass --role owner, admin, writer, member, contributor or reader.")
	}
	var member memberOutput
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		resp, err := s.Auth().SetAccountMember(s.Ctx(), &corev1.SetAccountMemberRequest{
			Account:  strings.TrimSpace(account),
			Username: strings.TrimSpace(username),
			Role:     normalizedRole,
		})
		if err != nil {
			return accountError(err)
		}
		member = memberOutput{Username: resp.Member.GetUsername(), SubjectID: resp.Member.GetSubjectId(), Role: resp.Member.GetRole()}
		return nil
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, member)
	}
	if !opts.Quiet {
		fmt.Fprintf(app.Stdout(), "%s is now %s of %s\n", member.Username, member.Role, strings.TrimSpace(account))
	}
	return nil
}

func runRemoveMember(app clibase.App, ctx context.Context, opts clibase.Options, account, username string, yes bool) error {
	if !yes {
		return clibase.UserError("confirmation_required", "remove-member requires --yes", "Run gs account remove-member <account> <username> --yes.")
	}
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		_, err := s.Auth().RemoveAccountMember(s.Ctx(), &corev1.RemoveAccountMemberRequest{
			Account:  strings.TrimSpace(account),
			Username: strings.TrimSpace(username),
		})
		return accountError(err)
	}); err != nil {
		return err
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, map[string]any{"account": strings.TrimSpace(account), "username": strings.TrimSpace(username), "removed": true})
	}
	if !opts.Quiet {
		fmt.Fprintf(app.Stdout(), "removed %s from %s\n", strings.TrimSpace(username), strings.TrimSpace(account))
	}
	return nil
}

func printMembers(w io.Writer, members []memberOutput) {
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
		fmt.Fprintln(w, line)
	}
}

func membersFromProto(members []*corev1.AccountMember) []memberOutput {
	out := make([]memberOutput, 0, len(members))
	for _, member := range members {
		out = append(out, memberOutput{
			Username:    member.GetUsername(),
			SubjectID:   member.GetSubjectId(),
			Role:        member.GetRole(),
			DisplayName: member.GetDisplayName(),
			Kind:        member.GetKind(),
		})
	}
	return out
}
