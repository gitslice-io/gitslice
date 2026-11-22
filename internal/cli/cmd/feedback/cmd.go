package feedback

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/cli/clibase"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type output struct {
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

type listOutput struct {
	Feedback   []output `json:"feedback"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type sendOptions struct {
	category string
	stdin    bool
	context  []string
}

// NewCommand builds the `gs feedback` command group.
func NewCommand(app clibase.App, opts *clibase.Options) *cobra.Command {
	send := sendOptions{}
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
			return runSend(app, cmd.Context(), *opts, args, send)
		},
	}
	addSendFlags(feedbackCmd)

	sendCmd := &cobra.Command{
		Use:   "send [message]",
		Short: "Send feedback (the same as gs feedback [message])",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSend(app, cmd.Context(), *opts, args, send)
		},
	}
	addSendFlags(sendCmd)

	var listMine bool
	var listStatus, listCursor string
	var listLimit int
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List feedback: yours with --mine, everyone's for the Gitslice team",
		Args:  clibase.NoArgs("gs feedback list [--mine] [--status open|resolved|all] [--limit N] [--cursor C]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			status := listStatus
			if !cmd.Flags().Changed("status") {
				status = "open"
				if listMine {
					status = "all"
				}
			}
			return runList(app, cmd.Context(), *opts, &corev1.ListFeedbackRequest{
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
		Args:  clibase.ExactArgs(1, "gs feedback resolve <feedback-id> --note <answer> | --reopen"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResolve(app, cmd.Context(), *opts, &corev1.ResolveFeedbackRequest{
				Id: args[0], Resolution: resolveNote, Reopen: resolveReopen,
			})
		},
	}
	resolveCmd.Flags().StringVar(&resolveNote, "note", "", "the answer the sender reads: what changed and where, or why nothing will")
	resolveCmd.Flags().BoolVar(&resolveReopen, "reopen", false, "open it again instead")

	clibase.WithSchemaOutput[listOutput](listCmd)
	feedbackCmd.AddCommand(sendCmd, listCmd, resolveCmd)
	return feedbackCmd
}

func runSend(app clibase.App, ctx context.Context, opts clibase.Options, args []string, send sendOptions) error {
	body := strings.TrimSpace(strings.Join(args, " "))
	if send.stdin {
		if body != "" {
			return clibase.UserError("invalid_args", "pass the feedback as an argument or with --stdin, not both", "")
		}
		data, err := io.ReadAll(app.Stdin())
		if err != nil {
			return err
		}
		body = strings.TrimSpace(string(data))
	}
	if body == "" {
		return clibase.UserError("message_required", "feedback message required", "Run gs feedback \"what went wrong or what would help\", or pipe it in with --stdin.")
	}
	feedbackContext := map[string]string{
		"gs_version": app.Version(),
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
	}
	if ws, err := app.ReadWorkspaceConfig(); err == nil && ws.Account != "" {
		feedbackContext["workspace"] = ws.Account + "/" + ws.Slice
	}
	for _, pair := range send.context {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return clibase.UserError("invalid_args", "--context takes key=value, got "+pair, "Pass --context command=\"gs sync\".")
		}
		feedbackContext[strings.TrimSpace(key)] = value
	}
	var sent *corev1.Feedback
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		var err error
		sent, err = s.Feedback().SubmitFeedback(s.Ctx(), &corev1.SubmitFeedbackRequest{
			Body: body, Category: send.category, Context: feedbackContext,
		})
		return feedbackError(err)
	}); err != nil {
		return err
	}
	out := toOutput(sent)
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(app.Stdout(), "sent feedback %s (%s). Thank you.\n", out.ID, out.Category)
	fmt.Fprintln(app.Stdout(), "The Gitslice team reads every piece; gs feedback list --mine shows their answer.")
	return nil
}

func runList(app clibase.App, ctx context.Context, opts clibase.Options, req *corev1.ListFeedbackRequest) error {
	var res *corev1.ListFeedbackResponse
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		var err error
		res, err = s.Feedback().ListFeedback(s.Ctx(), req)
		if err != nil {
			if grpcstatus.Code(err) == codes.PermissionDenied && !req.GetMine() {
				return clibase.UserError("feedback_team_only", grpcstatus.Convert(err).Message(), "Run gs feedback list --mine for your own feedback and the team's answers.")
			}
			return feedbackError(err)
		}
		return nil
	}); err != nil {
		return err
	}
	out := listOutput{Feedback: []output{}, NextCursor: res.GetNextCursor()}
	for _, f := range res.GetFeedback() {
		out.Feedback = append(out.Feedback, toOutput(f))
	}
	return clibase.WriteSchemaOutput[listOutput](app.Stdout(), opts, out, func() error {
		if len(out.Feedback) == 0 {
			fmt.Fprintln(app.Stdout(), "no feedback")
			return nil
		}
		for i, f := range out.Feedback {
			if i > 0 {
				fmt.Fprintln(app.Stdout())
			}
			writeText(app.Stdout(), f)
		}
		if out.NextCursor != "" {
			fmt.Fprintf(app.Stdout(), "\nmore: gs feedback list --cursor %s\n", out.NextCursor)
		}
		return nil
	})
}

func writeText(w io.Writer, f output) {
	from := clibase.FirstNonEmpty(f.Account, f.SubjectID)
	fmt.Fprintf(w, "%s  %s  %s  %s  from %s\n", f.ID, f.Status, f.Category, formatTime(f.CreatedAt), from)
	for _, line := range strings.Split(f.Body, "\n") {
		fmt.Fprintf(w, "  %s\n", line)
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
		fmt.Fprintf(w, "  context: %s\n", strings.Join(pairs, " "))
	}
	if f.Resolution != "" {
		fmt.Fprintf(w, "  answer from %s, %s:\n", clibase.FirstNonEmpty(f.ResolvedBy, "the team"), formatTime(f.ResolvedAt))
		for _, line := range strings.Split(f.Resolution, "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
}

func formatTime(value string) string {
	if len(value) >= len("2006-01-02T15:04") {
		return strings.Replace(value[:len("2006-01-02T15:04")], "T", " ", 1) + " UTC"
	}
	return value
}

func runResolve(app clibase.App, ctx context.Context, opts clibase.Options, req *corev1.ResolveFeedbackRequest) error {
	if req.GetReopen() && strings.TrimSpace(req.GetResolution()) != "" {
		return clibase.UserError("invalid_args", "use --note or --reopen, not both", "")
	}
	if !req.GetReopen() && strings.TrimSpace(req.GetResolution()) == "" {
		return clibase.UserError("note_required", "resolving feedback needs --note", "Say what changed and where (for example \"progress lines ship in v0.4.4\"), or why nothing will.")
	}
	var res *corev1.Feedback
	if err := app.WithSession(ctx, func(s clibase.Session) error {
		var err error
		res, err = s.Feedback().ResolveFeedback(s.Ctx(), req)
		if err != nil {
			if grpcstatus.Code(err) == codes.PermissionDenied {
				return clibase.UserError("feedback_team_only", grpcstatus.Convert(err).Message(), "")
			}
			return feedbackError(err)
		}
		return nil
	}); err != nil {
		return err
	}
	out := toOutput(res)
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if req.GetReopen() {
		fmt.Fprintf(app.Stdout(), "reopened %s\n", out.ID)
		return nil
	}
	fmt.Fprintf(app.Stdout(), "resolved %s; %s sees the answer in gs feedback list --mine\n", out.ID, clibase.FirstNonEmpty(out.Account, out.SubjectID))
	return nil
}

func feedbackError(err error) error {
	switch grpcstatus.Code(err) {
	case codes.Unimplemented:
		return clibase.UserError("feedback_unavailable", "this server does not take feedback yet", "Upgrade the server, or send feedback to the Gitslice team another way.")
	case codes.ResourceExhausted, codes.InvalidArgument, codes.NotFound:
		return clibase.UserError("feedback_rejected", grpcstatus.Convert(err).Message(), "")
	}
	return err
}

func toOutput(f *corev1.Feedback) output {
	return output{
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
