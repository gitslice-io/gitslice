package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// gs slice mirror: Git mirrors (design/35_git_mirrors.md).
func (r Runner) sliceMirrorCommand(opts *commandOptions) *cobra.Command {
	mirrorCmd := &cobra.Command{
		Use:   "mirror <slice|account:slice>",
		Short: "Show, set or push the slice's Git mirror (a copy on GitHub or elsewhere)",
		Long: "A slice can keep a copy of itself in a Git repository elsewhere, such as GitHub, as a backup\n" +
			"and for people who read code there. The server pushes every landing and every tag within\n" +
			"moments, fast-forward only: it stops with an error if the remote has commits that did not\n" +
			"come from the slice. A slice with one root folder is pushed with that folder as the\n" +
			"repository root.\n\n" +
			"The token is a secret of the slice's account that its folders see, for example:\n" +
			"  gs slice secret set acme/app GITHUB_MIRROR_TOKEN < token.txt\n" +
			"  gs slice mirror set acme/app --url https://github.com/acme/app.git --token-secret GITHUB_MIRROR_TOKEN\n" +
			"For GitHub, use a fine-grained token with Contents read and write on that one repository.\n" +
			"Owners and admins of the slice's account manage mirrors.",
		Args: exactArgs(1, "gs slice mirror <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceMirrorShow(cmd.Context(), *opts, args[0])
		},
	}
	var url, branch, username, tokenSecret string
	setCmd := &cobra.Command{
		Use:   "set <slice|account:slice> --url <https-url> [--token-secret NAME] [--branch main] [--username name]",
		Short: "Mirror the slice to a Git repository, replacing any mirror it has",
		Args:  exactArgs(1, "gs slice mirror set <slice|account:slice> --url https://github.com/acme/app.git --token-secret GITHUB_MIRROR_TOKEN"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				return userError("invalid_args", "--url is required", "Pass the repository's https URL, such as --url https://github.com/acme/app.git.")
			}
			return r.runSliceMirrorSet(cmd.Context(), *opts, args[0], &corev1.SetSliceGitMirrorRequest{Url: url, Branch: branch, Username: username, TokenSecret: tokenSecret})
		},
	}
	setCmd.Flags().StringVar(&url, "url", "", "the repository's https URL")
	setCmd.Flags().StringVar(&tokenSecret, "token-secret", "", "name of the account secret holding the access token (none: push without credentials)")
	setCmd.Flags().StringVar(&branch, "branch", "", "remote branch that receives the slice's history (default main)")
	setCmd.Flags().StringVar(&username, "username", "", "user name sent with the token (default x-access-token, for GitHub)")
	pushCmd := &cobra.Command{
		Use:   "push <slice|account:slice>",
		Short: "Push the slice's mirror now, without waiting for a retry",
		Args:  exactArgs(1, "gs slice mirror push <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceMirrorPush(cmd.Context(), *opts, args[0])
		},
	}
	removeCmd := &cobra.Command{
		Use:     "rm <slice|account:slice>",
		Aliases: []string{"remove", "delete"},
		Short:   "Stop mirroring the slice (the remote keeps what it has)",
		Args:    exactArgs(1, "gs slice mirror rm <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceMirrorRemove(cmd.Context(), *opts, args[0])
		},
	}
	mirrorCmd.AddCommand(setCmd, pushCmd, removeCmd)
	return mirrorCmd
}

func (r Runner) sliceMirrorClient(ctx context.Context, sliceRef string) (corev1.SliceServiceClient, *corev1.SliceRef, context.Context, func(), error) {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		conn.Close()
		return nil, nil, nil, nil, err
	}
	return corev1.NewSliceServiceClient(conn), ref, callCtx, func() { conn.Close() }, nil
}

func noMirrorError(sliceRef string) error {
	return userError("mirror_not_found", "slice "+sliceRef+" has no Git mirror", "Set one with gs slice mirror set "+sliceRef+" --url https://github.com/<owner>/<repo>.git --token-secret <NAME>.")
}

func (r Runner) runSliceMirrorShow(ctx context.Context, opts commandOptions, sliceRef string) error {
	client, ref, callCtx, done, err := r.sliceMirrorClient(ctx, sliceRef)
	if err != nil {
		return err
	}
	defer done()
	mirror, err := client.GetSliceGitMirror(callCtx, &corev1.GetSliceGitMirrorRequest{Slice: ref})
	if grpcstatus.Code(err) == codes.NotFound {
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, map[string]any{"mirror": nil})
		}
		return noMirrorError(sliceRef)
	}
	if err != nil {
		return err
	}
	return r.writeSliceMirror(opts, mirror, "")
}

func (r Runner) runSliceMirrorSet(ctx context.Context, opts commandOptions, sliceRef string, req *corev1.SetSliceGitMirrorRequest) error {
	client, ref, callCtx, done, err := r.sliceMirrorClient(ctx, sliceRef)
	if err != nil {
		return err
	}
	defer done()
	req.Slice = ref
	mirror, err := client.SetSliceGitMirror(callCtx, req)
	if grpcstatus.Code(err) == codes.FailedPrecondition {
		return userError("secret_not_found", grpcstatus.Convert(err).Message(), "Create the secret first, for example: gs slice secret set "+sliceRef+" "+req.TokenSecret+" < token.txt")
	}
	if err != nil {
		return err
	}
	return r.writeSliceMirror(opts, mirror, "mirror set; the first push starts in a moment")
}

func (r Runner) runSliceMirrorPush(ctx context.Context, opts commandOptions, sliceRef string) error {
	client, ref, callCtx, done, err := r.sliceMirrorClient(ctx, sliceRef)
	if err != nil {
		return err
	}
	defer done()
	mirror, err := client.PushSliceGitMirror(callCtx, &corev1.PushSliceGitMirrorRequest{Slice: ref})
	if grpcstatus.Code(err) == codes.NotFound {
		return noMirrorError(sliceRef)
	}
	if err != nil {
		return err
	}
	return r.writeSliceMirror(opts, mirror, "push queued; run gs slice mirror "+sliceRef+" to see how it went")
}

func (r Runner) runSliceMirrorRemove(ctx context.Context, opts commandOptions, sliceRef string) error {
	client, ref, callCtx, done, err := r.sliceMirrorClient(ctx, sliceRef)
	if err != nil {
		return err
	}
	defer done()
	if _, err := client.DeleteSliceGitMirror(callCtx, &corev1.DeleteSliceGitMirrorRequest{Slice: ref}); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"removed": true})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "stopped mirroring %s; the remote keeps what it has\n", sliceRef)
	}
	return nil
}

type sliceMirrorOutput struct {
	Slice                  string `json:"slice"`
	URL                    string `json:"url"`
	Branch                 string `json:"branch"`
	Username               string `json:"username,omitempty"`
	TokenSecret            string `json:"token_secret,omitempty"`
	Root                   string `json:"root,omitempty"`
	Pending                bool   `json:"pending"`
	LastAttemptAt          string `json:"last_attempt_at,omitempty"`
	LastPushedAt           string `json:"last_pushed_at,omitempty"`
	LastPushedCommit       string `json:"last_pushed_commit,omitempty"`
	LastPushedNativeCommit string `json:"last_pushed_native_commit,omitempty"`
	LastError              string `json:"last_error,omitempty"`
	NextAttemptAt          string `json:"next_attempt_at,omitempty"`
	Failures               int32  `json:"failures,omitempty"`
}

func (r Runner) writeSliceMirror(opts commandOptions, m *corev1.SliceGitMirror, note string) error {
	out := sliceMirrorOutput{
		Slice: sliceRefLabel(m.GetSlice()), URL: m.GetUrl(), Branch: m.GetBranch(), Username: m.GetUsername(),
		TokenSecret: m.GetTokenSecret(), Root: m.GetRoot(), Pending: m.GetPending(),
		LastAttemptAt: m.GetLastAttemptAt(), LastPushedAt: m.GetLastPushedAt(), LastPushedCommit: m.GetLastPushedCommit(),
		LastPushedNativeCommit: m.GetLastPushedNativeCommit(), LastError: m.GetLastError(), NextAttemptAt: m.GetNextAttemptAt(),
		Failures: m.GetFailures(),
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if note != "" {
		fmt.Fprintln(r.Stdout, note)
	}
	writeSliceMirrorText(r.Stdout, out)
	return nil
}

func writeSliceMirrorText(w io.Writer, m sliceMirrorOutput) {
	fmt.Fprintf(w, "mirror:  %s (branch %s)\n", m.URL, m.Branch)
	if m.Root != "" {
		fmt.Fprintf(w, "root:    %s becomes the repository root\n", m.Root)
	}
	token := "none (pushes without credentials)"
	if m.TokenSecret != "" {
		token = "secret " + m.TokenSecret
		if m.Username != "" {
			token += " as " + m.Username
		}
	}
	fmt.Fprintf(w, "token:   %s\n", token)
	switch {
	case m.LastError != "" && m.NextAttemptAt != "":
		fmt.Fprintf(w, "status:  failing (%d in a row); next try %s\n", m.Failures, m.NextAttemptAt)
	case m.Pending:
		fmt.Fprintln(w, "status:  push pending")
	default:
		fmt.Fprintln(w, "status:  up to date")
	}
	if m.LastPushedCommit != "" {
		fmt.Fprintf(w, "pushed:  %s at %s\n", shortMirrorCommit(m.LastPushedCommit), m.LastPushedAt)
	}
	if m.LastError != "" {
		fmt.Fprintf(w, "error:   %s\n", m.LastError)
	}
}

func shortMirrorCommit(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
