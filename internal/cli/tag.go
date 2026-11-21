package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
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
	markSchemaGroup(tagCmd)
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
	withSchemaOutput[tagOutput](createCmd)
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

func (r Runner) runTagCreate(ctx context.Context, opts commandOptions, slice, name, commitID, message string) error {
	return r.withSliceSession(ctx, slice, true, func(s *Session, ref *corev1.SliceRef) error {
		tag, err := s.Slice().CreateTag(s.Ctx(), &corev1.CreateTagRequest{
			Slice:    ref,
			Name:     strings.TrimSpace(name),
			CommitId: strings.TrimSpace(commitID),
			Message:  message,
		})
		if err != nil {
			return tagError(err)
		}
		out := tagToOutput(tag)
		return writeSchemaOutput[tagOutput](r, opts, out, func() error {
			fmt.Fprintf(r.Stdout, "tagged %s as %s in %s\n", shortID(out.CommitID), out.Name, out.Slice)
			return nil
		})
	})
}

func (r Runner) runTagList(ctx context.Context, opts commandOptions, slice string) error {
	return r.withSliceSession(ctx, slice, true, func(s *Session, ref *corev1.SliceRef) error {
		resp, err := s.Slice().ListTags(s.Ctx(), &corev1.ListTagsRequest{Slice: ref})
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
			if ref.GetSlice() == "home" && tag.Slice != "" {
				fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%s\n", tag.Name, tag.Slice, shortID(tag.CommitID), tag.CreatedAt)
			} else {
				fmt.Fprintf(r.Stdout, "%s\t%s\t%s\n", tag.Name, shortID(tag.CommitID), tag.CreatedAt)
			}
		}
		return nil
	})
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

var tagErrorHints = domainErrorOverrides{
	codes.AlreadyExists: {
		Code: "tag_exists",
		Hint: "Tags are immutable; pick a new name.",
	},
	codes.PermissionDenied: {
		Hint: "Creating tags needs write access to the slice.",
	},
}

func tagError(err error) error {
	return withDomainErrorHints(err, tagErrorHints)
}
