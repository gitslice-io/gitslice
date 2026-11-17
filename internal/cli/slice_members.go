package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// Slice members (design/26_slice_members.md): share one slice with a person
// or agent, without adding them to the account that owns it.

type sliceMemberOutput struct {
	Username    string `json:"username"`
	SubjectID   string `json:"subject_id"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	AddedBy     string `json:"added_by,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type sliceGroupMemberOutput struct {
	GroupID   string `json:"group_id"`
	Account   string `json:"account"`
	Name      string `json:"name"`
	Handle    string `json:"handle"`
	Role      string `json:"role"`
	AddedBy   string `json:"added_by,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type sliceMembersOutput struct {
	Slice        string                   `json:"slice"`
	Members      []sliceMemberOutput      `json:"members"`
	GroupMembers []sliceGroupMemberOutput `json:"group_members,omitempty"`
}

type sharedSliceOutput struct {
	sliceOutput
	Role string `json:"role"`
}

func (r Runner) sliceMemberCommands(opts *commandOptions) []*cobra.Command {
	var shareRole string
	shareCmd := &cobra.Command{
		Use:   "share <slice|account:slice> <username|@account/group>",
		Short: "Give a person, agent, or account group a role on one slice (writer by default)",
		Long: "Give a person, agent, or account group (@account/name) a role on one slice, without adding them to the account that owns it:\n" +
			"reader (read and clone), contributor (propose changesets and patchsets with secretless CI),\n" +
			"writer (submit, approve, tag and run CI with secrets) or admin (also manage the slice's\n" +
			"members and webhooks). Changing the role runs share again. The slice's definition, CI daemon\n" +
			"and secrets stay with the account's owners and admins.",
		Args: exactArgs(2, "gs slice share <slice|account:slice> <username|@account/group> [--role reader|contributor|writer|admin]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceShare(cmd.Context(), *opts, args[0], args[1], shareRole)
		},
	}
	shareCmd.Flags().StringVar(&shareRole, "role", "writer", "reader, contributor, writer or admin")

	unshareCmd := &cobra.Command{
		Use:     "unshare <slice|account:slice> <username|@account/group>",
		Aliases: []string{"revoke"},
		Short:   "Take away a user's, agent's, or group's role on a slice",
		Args:    exactArgs(2, "gs slice unshare <slice|account:slice> <username|@account/group>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceRemoveMember(cmd.Context(), *opts, args[0], args[1])
		},
	}
	leaveCmd := &cobra.Command{
		Use:   "leave <slice|account:slice>",
		Short: "Give up your role on a slice shared with you",
		Args:  exactArgs(1, "gs slice leave <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceRemoveMember(cmd.Context(), *opts, args[0], "")
		},
	}
	membersCmd := &cobra.Command{
		Use:   "members <slice|account:slice>",
		Short: "List who a slice is shared with",
		Args:  exactArgs(1, "gs slice members <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceMembers(cmd.Context(), *opts, args[0])
		},
	}
	return []*cobra.Command{shareCmd, unshareCmd, leaveCmd, membersCmd}
}

func (r Runner) sliceMemberConn(ctx context.Context, value string) (UserConfig, corev1.SliceServiceClient, context.Context, *corev1.SliceRef, func(), error) {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return UserConfig{}, nil, nil, nil, nil, err
	}
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, value)
	if err != nil {
		conn.Close()
		return UserConfig{}, nil, nil, nil, nil, err
	}
	return cfg, corev1.NewSliceServiceClient(conn), callCtx, ref, func() { conn.Close() }, nil
}

func (r Runner) runSliceShare(ctx context.Context, opts commandOptions, sliceValue, username, role string) error {
	normalizedRole := strings.ToLower(strings.TrimSpace(role))
	if storage.SliceRoleRank(normalizedRole) >= len(storage.SliceRoles) {
		return userError("invalid_role", fmt.Sprintf("role must be one of %s", strings.Join(storage.SliceRoles, ", ")), "Pass --role reader, contributor, writer or admin.")
	}
	_, client, callCtx, ref, done, err := r.sliceMemberConn(ctx, sliceValue)
	if err != nil {
		return err
	}
	defer done()
	member, err := client.SetSliceMember(callCtx, &corev1.SetSliceMemberRequest{Slice: ref, Username: strings.TrimSpace(username), Role: normalizedRole})
	if err != nil {
		return sliceMemberError(err, ref)
	}
	out := sliceMemberFromProto(member)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	label := ref.Account + "/" + ref.Slice
	fmt.Fprintf(r.Stdout, "shared %s with %s as %s\n", label, out.Username, out.Role)
	if ref.Slice == "home" {
		fmt.Fprintf(r.Stdout, "note: %s/home covers everything in /%s; a narrower slice shares less\n", ref.Account, ref.Account)
	}
	if strings.HasPrefix(out.Username, "@") {
		fmt.Fprintf(r.Stdout, "members of %s can now run: gs init %s\n", out.Username, label)
	} else {
		fmt.Fprintf(r.Stdout, "%s can now run: gs init %s\n", out.Username, label)
	}
	return nil
}

func (r Runner) runSliceRemoveMember(ctx context.Context, opts commandOptions, sliceValue, username string) error {
	_, client, callCtx, ref, done, err := r.sliceMemberConn(ctx, sliceValue)
	if err != nil {
		return err
	}
	defer done()
	if _, err := client.RemoveSliceMember(callCtx, &corev1.RemoveSliceMemberRequest{Slice: ref, Username: strings.TrimSpace(username)}); err != nil {
		return sliceMemberError(err, ref)
	}
	label := ref.Account + "/" + ref.Slice
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"slice": label, "removed": firstNonEmpty(strings.TrimSpace(username), "you")})
	}
	if opts.Quiet {
		return nil
	}
	if strings.TrimSpace(username) == "" {
		fmt.Fprintf(r.Stdout, "left %s\n", label)
		return nil
	}
	fmt.Fprintf(r.Stdout, "%s no longer has a role on %s\n", strings.TrimSpace(username), label)
	return nil
}

func (r Runner) runSliceMembers(ctx context.Context, opts commandOptions, sliceValue string) error {
	_, client, callCtx, ref, done, err := r.sliceMemberConn(ctx, sliceValue)
	if err != nil {
		return err
	}
	defer done()
	res, err := client.ListSliceMembers(callCtx, &corev1.ListSliceMembersRequest{Slice: ref})
	if err != nil {
		return sliceMemberError(err, ref)
	}
	label := ref.Account + "/" + ref.Slice
	out := sliceMembersOutput{
		Slice:        label,
		Members:      []sliceMemberOutput{},
		GroupMembers: []sliceGroupMemberOutput{},
	}
	for _, member := range res.GetMembers() {
		out.Members = append(out.Members, sliceMemberFromProto(member))
	}
	for _, groupMember := range res.GetGroupMembers() {
		out.GroupMembers = append(out.GroupMembers, sliceGroupMemberOutput{
			GroupID:   groupMember.GetGroupId(),
			Account:   groupMember.GetAccount(),
			Name:      groupMember.GetName(),
			Handle:    groupMember.GetHandle(),
			Role:      groupMember.GetRole(),
			AddedBy:   groupMember.GetAddedBy(),
			CreatedAt: groupMember.GetCreatedAt(),
		})
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if len(out.Members) == 0 && len(out.GroupMembers) == 0 {
		fmt.Fprintf(r.Stdout, "%s is not shared with anyone outside account %s\n", label, ref.Account)
	} else {
		for _, member := range out.Members {
			notes := []string{}
			if member.DisplayName != "" {
				notes = append(notes, member.DisplayName)
			}
			if member.Kind != "" {
				notes = append(notes, member.Kind)
			}
			if member.AddedBy != "" {
				notes = append(notes, "added by "+member.AddedBy)
			}
			fmt.Fprintf(r.Stdout, "%s\t%s\t(%s)\n", firstNonEmpty(member.Username, member.SubjectID), member.Role, strings.Join(notes, ", "))
		}
		for _, groupMember := range out.GroupMembers {
			notes := []string{"group"}
			if groupMember.AddedBy != "" {
				notes = append(notes, "added by "+groupMember.AddedBy)
			}
			handle := groupMember.Handle
			if handle == "" && groupMember.Account != "" && groupMember.Name != "" {
				handle = storage.FormatGroupHandle(groupMember.Account, groupMember.Name)
			}
			fmt.Fprintf(r.Stdout, "%s\t%s\t(%s)\n", handle, groupMember.Role, strings.Join(notes, ", "))
		}
	}
	fmt.Fprintf(r.Stdout, "The members of account %s have access too: gs account members %s\n", ref.Account, ref.Account)
	return nil
}

// listSharedSlices lists the slices shared with the caller.
func listSharedSlices(ctx context.Context, client corev1.SliceServiceClient) ([]sharedSliceOutput, error) {
	res, err := client.ListSharedSlices(ctx, &corev1.ListSharedSlicesRequest{})
	if err != nil {
		if grpcstatus.Code(err) == codes.Unimplemented {
			return nil, nil
		}
		return nil, err
	}
	out := make([]sharedSliceOutput, 0, len(res.GetSlices()))
	for _, shared := range res.GetSlices() {
		out = append(out, sharedSliceOutput{sliceOutput: sliceToOutput(shared.GetSlice()), Role: shared.GetRole()})
	}
	return out, nil
}

func (r Runner) writeSharedSlicesText(shared []sharedSliceOutput) {
	fmt.Fprintln(r.Stdout, "shared with you:")
	for _, slice := range shared {
		fmt.Fprintf(r.Stdout, "  %s (%s)\n", slice.Ref, slice.Role)
		fmt.Fprintf(r.Stdout, "    included paths: %s\n", strings.Join(slice.IncludedPaths, ", "))
	}
}

func sliceMemberFromProto(m *corev1.SliceMember) sliceMemberOutput {
	return sliceMemberOutput{
		Username:    m.GetUsername(),
		SubjectID:   m.GetSubjectId(),
		Role:        m.GetRole(),
		DisplayName: m.GetDisplayName(),
		Kind:        m.GetKind(),
		AddedBy:     m.GetAddedBy(),
		CreatedAt:   m.GetCreatedAt(),
	}
}

func sliceMemberError(err error, ref *corev1.SliceRef) error {
	message := grpcstatus.Convert(err).Message()
	label := ref.GetAccount() + "/" + ref.GetSlice()
	switch grpcstatus.Code(err) {
	case codes.Unimplemented:
		return userError("slice_members_unavailable", "this server does not support slice members yet", "Upgrade the server, or share through an organization (gs account create-org).")
	case codes.PermissionDenied:
		return userError("permission_denied", message, "Sharing "+label+" takes an owner or admin of account "+ref.GetAccount()+" or of the slice.")
	case codes.NotFound, codes.InvalidArgument:
		return userError("slice_member_rejected", message, "")
	}
	return err
}
