package cli

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// gs feedback sends feedback about Gitslice to the Gitslice team, and lets
// senders read the team's answers and the team read and answer everyone's
// (design/25_feedback.md).

type feedbackOutput struct {
	ID         string            `json:"id"`
	SubjectID  string            `json:"subject_id"`
	Account    string            `json:"account,omitempty"`
	Body       string            `json:"body"`
	Category   string            `json:"category"`
	Context    map[string]string `json:"context,omitempty"`
	Status     string            `json:"status"`
	Resolution string            `json:"resolution,omitempty"`
	ResolvedBy string            `json:"resolved_by,omitempty"`
	ResolvedAt string            `json:"resolved_at,omitempty"`
	CreatedAt  string            `json:"created_at"`
}

type feedbackListOutput struct {
	Feedback   []feedbackOutput `json:"feedback"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type feedbackSendOptions struct {
	category string
	stdin    bool
	context  []string
}

func (r Runner) feedbackCommand(opts *commandOptions) *cobra.Command {
	send := feedbackSendOptions{}
	addSendFlags := func(cmd *cobra.Command) {
		cmd.Flags().StringVar(&send.category, "category", "", "bug, idea, docs or other (the default)")
		cmd.Flags().BoolVar(&send.stdin, "stdin", false, "read the feedback from stdin")
		cmd.Flags().StringArrayVar(&send.context, "context", nil, "extra context as key=value, such as command=\"gs sync\"; repeatable")
	}
	feedbackCmd := &cobra.Command{
		Use:   "feedback [message]",
		Short: "Tell the Gitslice team what went wrong or what would help",
		Long: "Send feedback about Gitslice (gs, the web app, the docs) to the Gitslice team. gs adds its\n" +
			"version, OS and workspace slice. The team reads it and answers; gs feedback list --mine shows\n" +
			"your feedback with their answers.\n\n" +
			"  gs feedback \"gs sync ran silent for 440 paths; show progress\" --category idea\n" +
			"  gs feedback --stdin --category bug < notes.md",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFeedbackSend(cmd.Context(), *opts, args, send)
		},
	}
	addSendFlags(feedbackCmd)

	sendCmd := &cobra.Command{
		Use:   "send [message]",
		Short: "Send feedback (the same as gs feedback [message])",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFeedbackSend(cmd.Context(), *opts, args, send)
		},
	}
	addSendFlags(sendCmd)

	var listMine bool
	var listStatus, listCursor string
	var listLimit int
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List feedback: yours with --mine, everyone's for the Gitslice team",
		Args:  noArgs("gs feedback list [--mine] [--status open|resolved|all] [--limit N] [--cursor C]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			status := listStatus
			if !cmd.Flags().Changed("status") {
				// The team works through what is open; senders want everything
				// they sent, answered or not.
				status = "open"
				if listMine {
					status = "all"
				}
			}
			return r.runFeedbackList(cmd.Context(), *opts, &corev1.ListFeedbackRequest{
				Mine: listMine, Status: status, Limit: int32(listLimit), Cursor: listCursor,
			})
		},
	}
	listCmd.Flags().BoolVar(&listMine, "mine", false, "only your own feedback, with the team's answers")
	listCmd.Flags().StringVar(&listStatus, "status", "", "open, resolved or all (default: open, or all with --mine)")
	listCmd.Flags().IntVar(&listLimit, "limit", 50, "at most this many (up to 200)")
	listCmd.Flags().StringVar(&listCursor, "cursor", "", "continue from a previous list's next_cursor")

	var resolveNote string
	var resolveReopen bool
	resolveCmd := &cobra.Command{
		Use:   "resolve <feedback-id> --note <answer>",
		Short: "Answer feedback and mark it resolved (the Gitslice team)",
		Args:  exactArgs(1, "gs feedback resolve <feedback-id> --note <answer> | --reopen"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runFeedbackResolve(cmd.Context(), *opts, &corev1.ResolveFeedbackRequest{
				Id: args[0], Resolution: resolveNote, Reopen: resolveReopen,
			})
		},
	}
	resolveCmd.Flags().StringVar(&resolveNote, "note", "", "the answer the sender reads: what changed and where, or why nothing will")
	resolveCmd.Flags().BoolVar(&resolveReopen, "reopen", false, "open it again instead")

	feedbackCmd.AddCommand(sendCmd, listCmd, resolveCmd)
	return feedbackCmd
}

func (r Runner) runFeedbackSend(ctx context.Context, opts commandOptions, args []string, send feedbackSendOptions) error {
	body := strings.TrimSpace(strings.Join(args, " "))
	if send.stdin {
		if body != "" {
			return userError("invalid_args", "pass the feedback as an argument or with --stdin, not both", "")
		}
		data, err := io.ReadAll(r.stdin())
		if err != nil {
			return err
		}
		body = strings.TrimSpace(string(data))
	}
	if body == "" {
		return userError("message_required", "feedback message required", "Run gs feedback \"what went wrong or what would help\", or pipe it in with --stdin.")
	}
	feedbackContext := map[string]string{
		"gs_version": cliVersionInfo().Version,
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
	}
	if ws, err := r.readWorkspaceConfig(); err == nil && ws.Account != "" {
		feedbackContext["workspace"] = ws.Account + "/" + ws.Slice
	}
	for _, pair := range send.context {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return userError("invalid_args", "--context takes key=value, got "+pair, "Pass --context command=\"gs sync\".")
		}
		feedbackContext[strings.TrimSpace(key)] = value
	}
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	sent, err := corev1.NewFeedbackServiceClient(conn).SubmitFeedback(callCtx, &corev1.SubmitFeedbackRequest{
		Body: body, Category: send.category, Context: feedbackContext,
	})
	if err != nil {
		return feedbackError(err)
	}
	out := feedbackToOutput(sent)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "sent feedback %s (%s). Thank you.\n", out.ID, out.Category)
	fmt.Fprintln(r.Stdout, "The Gitslice team reads every piece; gs feedback list --mine shows their answer.")
	return nil
}

func (r Runner) runFeedbackList(ctx context.Context, opts commandOptions, req *corev1.ListFeedbackRequest) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewFeedbackServiceClient(conn).ListFeedback(callCtx, req)
	if err != nil {
		if grpcstatus.Code(err) == codes.PermissionDenied && !req.GetMine() {
			return userError("feedback_team_only", grpcstatus.Convert(err).Message(), "Run gs feedback list --mine for your own feedback and the team's answers.")
		}
		return feedbackError(err)
	}
	out := feedbackListOutput{Feedback: []feedbackOutput{}, NextCursor: res.GetNextCursor()}
	for _, f := range res.GetFeedback() {
		out.Feedback = append(out.Feedback, feedbackToOutput(f))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if len(out.Feedback) == 0 {
		fmt.Fprintln(r.Stdout, "no feedback")
		return nil
	}
	for i, f := range out.Feedback {
		if i > 0 {
			fmt.Fprintln(r.Stdout)
		}
		r.writeFeedbackText(f)
	}
	if out.NextCursor != "" {
		fmt.Fprintf(r.Stdout, "\nmore: gs feedback list --cursor %s\n", out.NextCursor)
	}
	return nil
}

func (r Runner) writeFeedbackText(f feedbackOutput) {
	from := firstNonEmpty(f.Account, f.SubjectID)
	fmt.Fprintf(r.Stdout, "%s  %s  %s  %s  from %s\n", f.ID, f.Status, f.Category, formatFeedbackTime(f.CreatedAt), from)
	for _, line := range strings.Split(f.Body, "\n") {
		fmt.Fprintf(r.Stdout, "  %s\n", line)
	}
	if len(f.Context) > 0 {
		keys := make([]string, 0, len(f.Context))
		for key := range f.Context {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, key := range keys {
			pairs = append(pairs, key+"="+f.Context[key])
		}
		fmt.Fprintf(r.Stdout, "  context: %s\n", strings.Join(pairs, " "))
	}
	if f.Resolution != "" {
		fmt.Fprintf(r.Stdout, "  answer from %s, %s:\n", firstNonEmpty(f.ResolvedBy, "the team"), formatFeedbackTime(f.ResolvedAt))
		for _, line := range strings.Split(f.Resolution, "\n") {
			fmt.Fprintf(r.Stdout, "    %s\n", line)
		}
	}
}

func formatFeedbackTime(value string) string {
	if len(value) >= len("2006-01-02T15:04") {
		return strings.Replace(value[:len("2006-01-02T15:04")], "T", " ", 1) + " UTC"
	}
	return value
}

func (r Runner) runFeedbackResolve(ctx context.Context, opts commandOptions, req *corev1.ResolveFeedbackRequest) error {
	if req.GetReopen() && strings.TrimSpace(req.GetResolution()) != "" {
		return userError("invalid_args", "use --note or --reopen, not both", "")
	}
	if !req.GetReopen() && strings.TrimSpace(req.GetResolution()) == "" {
		return userError("note_required", "resolving feedback needs --note", "Say what changed and where (for example \"progress lines ship in v0.4.4\"), or why nothing will.")
	}
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewFeedbackServiceClient(conn).ResolveFeedback(callCtx, req)
	if err != nil {
		if grpcstatus.Code(err) == codes.PermissionDenied {
			return userError("feedback_team_only", grpcstatus.Convert(err).Message(), "")
		}
		return feedbackError(err)
	}
	out := feedbackToOutput(res)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if req.GetReopen() {
		fmt.Fprintf(r.Stdout, "reopened %s\n", out.ID)
		return nil
	}
	fmt.Fprintf(r.Stdout, "resolved %s; %s sees the answer in gs feedback list --mine\n", out.ID, firstNonEmpty(out.Account, out.SubjectID))
	return nil
}

func feedbackError(err error) error {
	switch grpcstatus.Code(err) {
	case codes.Unimplemented:
		return userError("feedback_unavailable", "this server does not take feedback yet", "Upgrade the server, or send feedback to the Gitslice team another way.")
	case codes.ResourceExhausted, codes.InvalidArgument, codes.NotFound:
		return userError("feedback_rejected", grpcstatus.Convert(err).Message(), "")
	}
	return err
}

func feedbackToOutput(f *corev1.Feedback) feedbackOutput {
	return feedbackOutput{
		ID:         f.GetId(),
		SubjectID:  f.GetSubjectId(),
		Account:    f.GetAccount(),
		Body:       f.GetBody(),
		Category:   f.GetCategory(),
		Context:    f.GetContext(),
		Status:     f.GetStatus(),
		Resolution: f.GetResolution(),
		ResolvedBy: f.GetResolvedBy(),
		ResolvedAt: f.GetResolvedAt(),
		CreatedAt:  f.GetCreatedAt(),
	}
}
