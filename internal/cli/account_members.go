package cli

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type accountMemberOutput struct {
	Username    string `json:"username"`
	SubjectID   string `json:"subject_id"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name,omitempty"`
	// Kind is person or agent.
	Kind string `json:"kind,omitempty"`
	// You marks the signed-in subject.
	You bool `json:"you,omitempty"`
	// ClaimedAgent marks a person who co-owns this account because they
	// claimed the agent it belongs to.
	ClaimedAgent bool `json:"claimed_agent,omitempty"`
}

type accountMembersOutput struct {
	Account string                `json:"account"`
	Kind    string                `json:"kind,omitempty"`
	Members []accountMemberOutput `json:"members"`
}

func (r Runner) runAccountMembers(ctx context.Context, opts commandOptions, account string) error {
	cfg, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).ListAccountMembers(authCtx, &corev1.ListAccountMembersRequest{Account: strings.TrimSpace(account)})
	if err != nil {
		if grpcstatus.Code(err) == codes.NotFound {
			return r.accountNotFoundError(authCtx, conn, strings.TrimSpace(account), err)
		}
		return accountError(err)
	}
	members := accountMembersFromProto(resp.Members)
	markAccountMembers(members, resp.Account, cfg.SubjectID)
	out := accountMembersOutput{Account: resp.Account, Kind: resp.Kind, Members: members}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	r.printAccountMembers(out.Members)
	return nil
}

// accountNotFoundError answers gs account members for an account the caller
// does not belong to. The server does not say whether the account exists, but
// the caller may still have slices of it shared with them
// (design/26_slice_members.md); say so rather than only "not found".
func (r Runner) accountNotFoundError(ctx context.Context, conn *grpc.ClientConn, account string, err error) error {
	message := grpcstatus.Convert(err).Message()
	shared, listErr := listSharedSlices(ctx, corev1.NewSliceServiceClient(conn))
	if listErr == nil {
		var refs, labels []string
		for _, s := range shared {
			if strings.HasPrefix(s.Ref, account+":") {
				refs = append(refs, s.Ref)
				labels = append(labels, fmt.Sprintf("%s (%s)", s.Ref, s.Role))
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

// markAccountMembers marks the signed-in subject, and in an agent's own
// account the people who claimed the agent: the account is the agent's when
// its slug is the username of an agent member.
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
	if storage.AccountRoleRank(normalizedRole) >= len(storage.AccountRoles) {
		return userError("invalid_role", fmt.Sprintf("role must be one of %s", strings.Join(storage.AccountRoles, ", ")), "Pass --role owner, admin, writer, member, contributor or reader.")
	}
	_, conn, authCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).SetAccountMember(authCtx, &corev1.SetAccountMemberRequest{
		Account:  strings.TrimSpace(account),
		Username: strings.TrimSpace(username),
		Role:     normalizedRole,
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
