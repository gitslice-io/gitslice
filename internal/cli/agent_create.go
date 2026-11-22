package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type agentCreateOptions struct {
	Slice       string
	Role        string
	DisplayName string
}

func (r Runner) agentCreateCommand(opts *commandOptions) *cobra.Command {
	var createOpts agentCreateOptions
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a delegated agent owned by you and print its API key once",
		Long: `Create a delegated agent owned and controlled by you, optionally add it to a
slice with a role (reader, contributor, writer, or admin; default contributor),
and print its initial API key once to stdout (with everything else on stderr).
With --json the output carries "secret".`,
		Args: exactArgs(1, "gs agent create <name> [--slice account/slice] [--role reader|contributor|writer|admin] [--display-name text] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAgentCreate(cmd.Context(), *opts, args[0], createOpts)
		},
	}
	cmd.Flags().StringVar(&createOpts.Slice, "slice", "", "add the new agent to a slice (<account>/<slice>)")
	cmd.Flags().StringVar(&createOpts.Role, "role", "", "role on --slice: reader, contributor (default when --slice is set), writer, or admin")
	cmd.Flags().StringVar(&createOpts.DisplayName, "display-name", "", "human-readable display name for the agent")
	return cmd
}

func (r Runner) runAgentCreate(ctx context.Context, opts commandOptions, name string, in agentCreateOptions) error {
	username := strings.TrimSpace(name)
	if username == "" {
		return userError("agent_name_required", "agent name is required", "Usage: gs agent create <name> [--slice account/slice] [--role reader|contributor|writer|admin] [--display-name text]")
	}
	sliceInput := strings.TrimSpace(in.Slice)
	roleInput := strings.TrimSpace(in.Role)
	if roleInput != "" && sliceInput == "" {
		return userError("slice_required", "--role requires --slice", "Pass --slice <account/slice> together with --role.")
	}
	normalizedRole := ""
	if sliceInput != "" {
		var err error
		normalizedRole, err = model.NormalizeDelegatedAgentSliceRole(sliceInput, roleInput)
		if err != nil {
			return userError("invalid_role", err.Error(), "Use reader, contributor, writer, or admin.")
		}
	}

	var resp *corev1.CreateDelegatedAgentResponse
	if err := r.withSession(ctx, func(s *Session) error {
		var sliceRef *corev1.SliceRef
		var err error
		if sliceInput != "" {
			sliceRef, err = s.ResolveSliceRef(sliceInput, false)
			if err != nil {
				return err
			}
		}
		resp, err = s.Auth().CreateDelegatedAgent(s.Ctx(), &corev1.CreateDelegatedAgentRequest{
			Username:    username,
			DisplayName: strings.TrimSpace(in.DisplayName),
			Slice:       sliceRef,
			Role:        normalizedRole,
		})
		return err
	}); err != nil {
		return err
	}

	key := apiKeyToOutput(resp.GetKey())
	if opts.JSONOutput() {
		out := map[string]any{
			"agent_subject_id": resp.GetAgentSubjectId(),
			"account":          resp.GetAccount(),
			"key":              key,
			"secret":           resp.GetSecret(),
		}
		if resp.GetSlice() != nil {
			out["slice"] = sliceRefLabel(resp.GetSlice())
			out["role"] = resp.GetRole()
		}
		return r.writeJSONOutput(opts, out)
	}

	if !opts.Quiet {
		fmt.Fprintf(r.stderr(), "created delegated agent %s (%s) with initial API key %s. The secret is shown once; the server keeps only a hash.\n",
			resp.GetAccount(), resp.GetAgentSubjectId(), key.ID)
		if resp.GetSlice() != nil {
			fmt.Fprintf(r.stderr(), "Added to %s as %s.\n", sliceRefLabel(resp.GetSlice()), resp.GetRole())
		}
		fmt.Fprintf(r.stderr(), "Use with GS_TOKEN or gs auth use-key.\n")
		fmt.Fprintf(r.stderr(), "Manage keys: gs auth keys list --agent %s\n", resp.GetAccount())
	}
	fmt.Fprintln(r.Stdout, resp.GetSecret())
	return nil
}
