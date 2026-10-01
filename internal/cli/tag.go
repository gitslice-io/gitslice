package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type tagOutput struct {
	Slice             string `json:"slice"`
	Name              string `json:"name"`
	CommitID          string `json:"commit_id"`
	DefinitionVersion int64  `json:"definition_version"`
	Message           string `json:"message,omitempty"`
	CreatedBy         string `json:"created_by,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
}

// tagCommand manages immutable slice tags, which the Git projection publishes
// as refs/tags/<name>.
func (r Runner) tagCommand(opts *commandOptions) *cobra.Command {
	tagCmd := &cobra.Command{
		Use:   "tag",
		Short: "Create and list immutable slice tags (releases)",
		RunE:  requireSubcommand("tag"),
	}
	createSlice, createCommit, createMessage := "", "", ""
	createCmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Tag a commit (default: the current head) for a slice",
		Args:  exactArgs(1, "gs tag create <name> [--slice account/slice] [--commit id] [--message text]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTagCreate(cmd.Context(), *opts, createSlice, args[0], createCommit, createMessage)
		},
	}
	createCmd.Flags().StringVar(&createSlice, "slice", createSlice, "slice to tag; defaults to the workspace slice")
	createCmd.Flags().StringVar(&createCommit, "commit", createCommit, "native commit id; defaults to the current head")
	createCmd.Flags().StringVarP(&createMessage, "message", "m", createMessage, "tag message")
	listSlice := ""
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List a slice's tags, newest first",
		Args:  noArgs("gs tag list [--slice account/slice]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTagList(cmd.Context(), *opts, listSlice)
		},
	}
	listCmd.Flags().StringVar(&listSlice, "slice", listSlice, "slice; defaults to the workspace slice")
	tagCmd.AddCommand(createCmd, listCmd)
	return tagCmd
}

func (r Runner) tagSliceRef(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, value string) (*corev1.SliceRef, error) {
	if strings.TrimSpace(value) != "" {
		return r.resolveSliceRefInput(ctx, cfg, conn, value)
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		return nil, userError("slice_required", "--slice is required outside a workspace", "Pass --slice account/slice.")
	}
	return &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}, nil
}

func (r Runner) runTagCreate(ctx context.Context, opts commandOptions, slice, name, commitID, message string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.tagSliceRef(callCtx, cfg, conn, slice)
	if err != nil {
		return err
	}
	tag, err := corev1.NewSliceServiceClient(conn).CreateTag(callCtx, &corev1.CreateTagRequest{
		Slice:    ref,
		Name:     strings.TrimSpace(name),
		CommitId: strings.TrimSpace(commitID),
		Message:  message,
	})
	if err != nil {
		return tagError(err)
	}
	out := tagToOutput(tag)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "tagged %s as %s in %s\n", shortID(out.CommitID), out.Name, out.Slice)
	}
	return nil
}

func (r Runner) runTagList(ctx context.Context, opts commandOptions, slice string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.tagSliceRef(callCtx, cfg, conn, slice)
	if err != nil {
		return err
	}
	resp, err := corev1.NewSliceServiceClient(conn).ListTags(callCtx, &corev1.ListTagsRequest{Slice: ref})
	if err != nil {
		return tagError(err)
	}
	out := make([]tagOutput, 0, len(resp.Tags))
	for _, tag := range resp.Tags {
		out = append(out, tagToOutput(tag))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"tags": out})
	}
	for _, tag := range out {
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\n", tag.Name, shortID(tag.CommitID), tag.CreatedAt)
	}
	return nil
}

func tagToOutput(tag *corev1.Tag) tagOutput {
	slice := ""
	if tag.GetSlice() != nil {
		slice = tag.Slice.Account + "/" + tag.Slice.Slice
	}
	return tagOutput{
		Slice:             slice,
		Name:              tag.GetName(),
		CommitID:          tag.GetCommitId(),
		DefinitionVersion: tag.GetDefinitionVersion(),
		Message:           tag.GetMessage(),
		CreatedBy:         tag.GetCreatedBy(),
		CreatedAt:         tag.GetCreatedAt(),
	}
}

func tagError(err error) error {
	message := grpcstatus.Convert(err).Message()
	switch grpcstatus.Code(err) {
	case codes.AlreadyExists:
		return userError("tag_exists", message, "Tags are immutable; pick a new name.")
	case codes.InvalidArgument:
		return userError("invalid_argument", message, "")
	case codes.PermissionDenied:
		return userError("permission_denied", message, "Creating tags needs write access to the slice.")
	case codes.NotFound:
		return userError("not_found", message, "")
	}
	return err
}
