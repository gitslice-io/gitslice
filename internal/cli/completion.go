package cli

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// gs completion <shell> prints the shell's completion script (cobra's).
// Commands and flags complete from the script; the arguments below complete
// from the server, so they stay current: changesets from the workspace's
// dependency tree, slices from the accounts you belong to. A completion that
// cannot reach the server in time offers nothing rather than hanging the
// shell.

const completionTimeout = 3 * time.Second

// maxCompletionAccounts bounds how many accounts slice completion lists.
const maxCompletionAccounts = 8

// attachCompletions completes the first argument of every command whose usage
// names a changeset or a slice there.
func (r Runner) attachCompletions(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		r.attachCompletions(child)
	}
	if cmd.ValidArgsFunction != nil {
		return
	}
	fields := strings.Fields(cmd.Use)
	if len(fields) < 2 {
		return
	}
	switch arg := strings.Trim(fields[1], "[]<>"); {
	case arg == "changeset":
		cmd.ValidArgsFunction = r.completeChangesets
	case strings.Contains(arg, "slice"):
		cmd.ValidArgsFunction = r.completeSlices
	}
}

func completionContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, completionTimeout)
}

// completeChangesets offers the changesets in the workspace's dependency tree,
// with their titles.
func (r Runner) completeChangesets(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, _, state, err := r.loadLocalState()
	if err != nil || state.ActiveStackID == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext(cmd)
	defer cancel()
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer conn.Close()
	stack, err := corev1.NewChangesetStackServiceClient(conn).GetStack(authContext(ctx, cfg), &corev1.GetStackRequest{StackId: state.ActiveStackID})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, entry := range stack.GetEntries() {
		id := storage.ShortChangesetID(entry.GetChangesetId())
		if id == "" || !strings.HasPrefix(id, toComplete) {
			continue
		}
		if title := entry.GetChangeset().GetTitle(); title != "" {
			id += "\t" + title
		}
		out = append(out, id)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeSlices offers account/slice for the slices of the accounts the
// signed-in subject belongs to.
func (r Runner) completeSlices(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := r.readUserConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext(cmd)
	defer cancel()
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	status, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(callCtx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	accounts := status.GetAccounts()
	// A prefix that names an account lists just that account.
	if account, _, ok := strings.Cut(strings.ReplaceAll(toComplete, ":", "/"), "/"); ok {
		accounts = []string{account}
	}
	if len(accounts) > maxCompletionAccounts {
		accounts = accounts[:maxCompletionAccounts]
	}
	client := corev1.NewSliceServiceClient(conn)
	var out []string
	for _, account := range accounts {
		slices, err := listAllSlices(callCtx, client, account)
		if err != nil {
			continue
		}
		for _, slice := range slices {
			ref := slice.GetRef().GetAccount() + "/" + slice.GetRef().GetSlice()
			if !strings.HasPrefix(ref, strings.ReplaceAll(toComplete, ":", "/")) {
				continue
			}
			out = append(out, ref)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
