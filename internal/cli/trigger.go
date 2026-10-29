package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// triggerCommand shows triggers and their runs, and runs them by hand
// (design/34_triggers.md).
func (r Runner) triggerCommand(opts *commandOptions) *cobra.Command {
	triggerCmd := &cobra.Command{
		Use:   "trigger",
		Short: "Show the triggers of a folder and their runs; run one by hand",
		Long: "A trigger is defined in <folder>/.gitslice/triggers.yaml and fires for changes in that folder and\n" +
			"below: an HTTP call, or a script run on Cloudflare that may report checks. See gs help triggers.",
		RunE: requireSubcommand("trigger"),
	}

	var lsRef string
	lsCmd := &cobra.Command{
		Use:     "ls [folder]",
		Aliases: []string{"list"},
		Short:   "List the triggers that apply at a folder (default: the current one)",
		Args:    maxArgs(1, "gs trigger ls [folder] [--ref <ref>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			folder := ""
			if len(args) == 1 {
				folder = args[0]
			}
			return r.runTriggerList(cmd.Context(), *opts, folder, lsRef)
		},
	}
	lsCmd.Flags().StringVar(&lsRef, "ref", "", "read the definitions at this ref (default refs/global/main)")

	var runsAccount, runsTrigger, runsChangeset string
	var runsLimit int
	runsCmd := &cobra.Command{
		Use:   "runs",
		Short: "List trigger runs of a changeset, a trigger or an account, newest first",
		Args:  noArgs("gs trigger runs [--changeset <id>] [--trigger <id>] [--account <account>] [--limit n]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerRuns(cmd.Context(), *opts, runsAccount, runsTrigger, runsChangeset, runsLimit)
		},
	}
	runsCmd.Flags().StringVar(&runsChangeset, "changeset", "", "runs about this changeset")
	runsCmd.Flags().StringVar(&runsTrigger, "trigger", "", "runs of this trigger id, such as services/billing/ci")
	runsCmd.Flags().StringVar(&runsAccount, "account", "", "the account (default: your active account)")
	runsCmd.Flags().IntVar(&runsLimit, "limit", 20, "how many runs")

	showCmd := &cobra.Command{
		Use:   "show <run-id>",
		Short: "Show a trigger run",
		Args:  exactArgs(1, "gs trigger show <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerShow(cmd.Context(), *opts, args[0])
		},
	}

	logsCmd := &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Print a trigger run's log",
		Args:  exactArgs(1, "gs trigger logs <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerLogs(cmd.Context(), *opts, args[0])
		},
	}

	var runAccount, runRef, runChangeset string
	var runInputs []string
	runCmd := &cobra.Command{
		Use:   "run <trigger-id>",
		Short: "Run a trigger that listens for manual",
		Args:  exactArgs(1, "gs trigger run <trigger-id> [--account <account>] [--ref <ref>] [--changeset <id>] [--input key=value]..."),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerRun(cmd.Context(), *opts, runAccount, args[0], runRef, runChangeset, runInputs)
		},
	}
	runCmd.Flags().StringVar(&runAccount, "account", "", "the account (default: your active account)")
	runCmd.Flags().StringVar(&runRef, "ref", "", "run the definition at this ref (default refs/global/main)")
	runCmd.Flags().StringVar(&runChangeset, "changeset", "", "a changeset the run is about; its checks may be reported")
	runCmd.Flags().StringArrayVar(&runInputs, "input", nil, "key=value passed as event.inputs (repeatable)")

	rerunCmd := &cobra.Command{
		Use:   "rerun <run-id>",
		Short: "Run a finished run's trigger again for the same event",
		Args:  exactArgs(1, "gs trigger rerun <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerRunAction(cmd.Context(), *opts, args[0], "rerun")
		},
	}
	cancelCmd := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel a run that has not finished",
		Args:  exactArgs(1, "gs trigger cancel <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerRunAction(cmd.Context(), *opts, args[0], "cancel")
		},
	}

	triggerCmd.AddCommand(lsCmd, runsCmd, showCmd, logsCmd, runCmd, rerunCmd, cancelCmd)
	return triggerCmd
}

type triggerOutput struct {
	ID          string   `json:"id"`
	Folder      string   `json:"folder"`
	On          []string `json:"on"`
	Paths       []string `json:"paths,omitempty"`
	Changes     []string `json:"changes,omitempty"`
	Action      string   `json:"action"`
	Worker      string   `json:"worker,omitempty"`
	URL         string   `json:"url,omitempty"`
	Checks      []string `json:"checks,omitempty"`
	Secrets     []string `json:"secrets,omitempty"`
	Network     []string `json:"network,omitempty"`
	Concurrency string   `json:"concurrency"`
}

type triggerRunOutput struct {
	ID          string `json:"id"`
	Trigger     string `json:"trigger"`
	Status      string `json:"status"`
	Event       string `json:"event"`
	EventID     string `json:"event_id"`
	Action      string `json:"action"`
	Changeset   string `json:"changeset,omitempty"`
	Attempt     int32  `json:"attempt"`
	MaxAttempts int32  `json:"max_attempts"`
	Error       string `json:"error,omitempty"`
	CreatedAt   string `json:"created_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
	Definition  string `json:"definition_commit,omitempty"`
}

func triggerRunToOutput(run *corev1.TriggerRun) triggerRunOutput {
	return triggerRunOutput{
		ID: run.Id, Trigger: run.TriggerId, Status: run.Status, Event: run.EventKind, EventID: run.EventId, Action: run.Action,
		Changeset: storage.ShortChangesetID(run.ChangesetId), Attempt: run.Attempt, MaxAttempts: run.MaxAttempts,
		Error: run.Error, CreatedAt: run.CreatedAt, FinishedAt: run.FinishedAt, Definition: run.DefinitionCommitId,
	}
}

// triggerFolder reads a folder argument: canonical (/acme/x), or a folder of
// the workspace (default: the current directory).
func (r Runner) triggerFolder(raw string) (string, error) {
	if strings.HasPrefix(strings.TrimSpace(raw), "/") {
		folder, err := paths.CanonicalPrefix(strings.TrimSpace(raw))
		if err != nil {
			return "", userError("invalid_argument", err.Error(), "")
		}
		return folder, nil
	}
	dir := r.cwd()
	if raw != "" {
		dir = filepath.Join(dir, raw)
	}
	root, err := workspaceRootFrom(dir)
	if err != nil {
		return "", userError("path_required", "not in a gitslice workspace", "Pass a canonical folder, such as /acme/services.")
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", userError("path_required", "cannot tell which folder that is", "Pass a canonical folder, such as /acme/services.")
	}
	folder, err := paths.CanonicalPrefix("/" + filepath.ToSlash(rel))
	if err != nil {
		return "", userError("invalid_argument", err.Error(), "")
	}
	return folder, nil
}

func (r Runner) runTriggerList(ctx context.Context, opts commandOptions, rawFolder, ref string) error {
	folder, err := r.triggerFolder(rawFolder)
	if err != nil {
		return err
	}
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewTriggerServiceClient(conn).ListTriggers(callCtx, &corev1.ListTriggersRequest{Path: folder, Ref: ref})
	if err != nil {
		return triggerError(err)
	}
	out := make([]triggerOutput, 0, len(resp.Triggers))
	for _, t := range resp.Triggers {
		out = append(out, triggerOutput{ID: t.Id, Folder: t.Folder, On: t.On, Paths: t.Paths, Changes: t.Changes, Action: t.Action,
			Worker: t.Worker, URL: t.HttpUrl, Checks: t.Checks, Secrets: t.Secrets, Network: t.Network, Concurrency: t.Concurrency})
	}
	if opts.jsonOutput() {
		problems := make([]map[string]string, 0, len(resp.Problems))
		for _, p := range resp.Problems {
			problems = append(problems, map[string]string{"file": p.File, "message": p.Message})
		}
		return r.writeJSONOutput(opts, map[string]any{"folder": folder, "commit": resp.CommitId, "triggers": out, "problems": problems})
	}
	if opts.Quiet {
		return nil
	}
	if len(out) == 0 && len(resp.Problems) == 0 {
		fmt.Fprintf(r.Stdout, "no triggers apply at %s\n", folder)
		return nil
	}
	for _, t := range out {
		what := t.Worker
		if t.Action == "http" {
			what = t.URL
		}
		line := fmt.Sprintf("%s\ton %s\t%s %s", t.ID, strings.Join(t.On, ","), t.Action, what)
		if len(t.Paths) > 0 {
			line += "\tpaths " + strings.Join(t.Paths, ",")
		}
		if len(t.Checks) > 0 {
			line += "\tchecks " + strings.Join(t.Checks, ",")
		}
		fmt.Fprintln(r.Stdout, line)
	}
	for _, p := range resp.Problems {
		fmt.Fprintf(r.stderr(), "%s: %s\n", p.File, p.Message)
	}
	return nil
}

func (r Runner) runTriggerRuns(ctx context.Context, opts commandOptions, account, trigger, changeset string, limit int) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	req := &corev1.ListTriggerRunsRequest{TriggerId: strings.TrimSpace(trigger), Limit: int32(limit)}
	if changeset != "" {
		id, err := fullChangesetID(callCtx, conn, changeset)
		if err != nil {
			return err
		}
		req.ChangesetId = id
	} else {
		if req.Account, err = r.secretAccount(callCtx, cfg, conn, account); err != nil {
			return err
		}
	}
	resp, err := corev1.NewTriggerServiceClient(conn).ListTriggerRuns(callCtx, req)
	if err != nil {
		return triggerError(err)
	}
	out := make([]triggerRunOutput, 0, len(resp.Runs))
	for _, run := range resp.Runs {
		out = append(out, triggerRunToOutput(run))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"runs": out})
	}
	if opts.Quiet {
		return nil
	}
	if len(out) == 0 {
		fmt.Fprintln(r.Stdout, "no trigger runs")
		return nil
	}
	for _, run := range out {
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", run.ID, run.Status, run.Trigger, run.Event, run.CreatedAt)
		if run.Error != "" {
			line += "\t" + oneLineSummary(run.Error)
		}
		fmt.Fprintln(r.Stdout, line)
	}
	return nil
}

func (r Runner) runTriggerShow(ctx context.Context, opts commandOptions, runID string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	run, err := corev1.NewTriggerServiceClient(conn).GetTriggerRun(callCtx, &corev1.GetTriggerRunRequest{RunId: strings.TrimSpace(runID)})
	if err != nil {
		return triggerError(err)
	}
	out := triggerRunToOutput(run)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "run: %s\ntrigger: %s (%s)\nstatus: %s\nevent: %s %s\nattempt: %d of %d\ncreated: %s\n",
		out.ID, out.Trigger, out.Action, out.Status, out.Event, out.EventID, out.Attempt, out.MaxAttempts, out.CreatedAt)
	if out.Changeset != "" {
		fmt.Fprintf(r.Stdout, "changeset: %s\n", out.Changeset)
	}
	if out.FinishedAt != "" {
		fmt.Fprintf(r.Stdout, "finished: %s\n", out.FinishedAt)
	}
	if out.Error != "" {
		fmt.Fprintf(r.Stdout, "error: %s\n", out.Error)
	}
	return nil
}

func (r Runner) runTriggerLogs(ctx context.Context, opts commandOptions, runID string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewTriggerServiceClient(conn)
	var after int64
	var all []*corev1.TriggerRunLog
	for {
		resp, err := client.ListTriggerRunLogs(callCtx, &corev1.ListTriggerRunLogsRequest{RunId: strings.TrimSpace(runID), AfterSeq: after})
		if err != nil {
			return triggerError(err)
		}
		if len(resp.Logs) == 0 {
			break
		}
		all = append(all, resp.Logs...)
		after = resp.Logs[len(resp.Logs)-1].Seq
	}
	if opts.jsonOutput() {
		lines := make([]map[string]any, 0, len(all))
		for _, l := range all {
			lines = append(lines, map[string]any{"seq": l.Seq, "stream": l.Stream, "chunk": l.Chunk, "created_at": l.CreatedAt})
		}
		return r.writeJSONOutput(opts, map[string]any{"logs": lines})
	}
	for _, l := range all {
		if l.Stream == "stderr" {
			fmt.Fprint(r.stderr(), l.Chunk)
		} else {
			fmt.Fprint(r.Stdout, l.Chunk)
		}
	}
	return nil
}

func (r Runner) runTriggerRun(ctx context.Context, opts commandOptions, account, triggerID, ref, changeset string, rawInputs []string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if account, err = r.secretAccount(callCtx, cfg, conn, account); err != nil {
		return err
	}
	inputs := map[string]string{}
	for _, raw := range rawInputs {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return userError("invalid_argument", "--input "+raw+" is not key=value", "")
		}
		inputs[strings.TrimSpace(key)] = value
	}
	req := &corev1.RunTriggerRequest{Account: account, TriggerId: strings.TrimSpace(triggerID), Ref: ref, Inputs: inputs}
	if changeset != "" {
		if req.ChangesetId, err = fullChangesetID(callCtx, conn, changeset); err != nil {
			return err
		}
	}
	run, err := corev1.NewTriggerServiceClient(conn).RunTrigger(callCtx, req)
	if err != nil {
		return triggerError(err)
	}
	return r.printTriggerRun(opts, "queued", run)
}

func (r Runner) runTriggerRunAction(ctx context.Context, opts commandOptions, runID, action string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewTriggerServiceClient(conn)
	var run *corev1.TriggerRun
	switch action {
	case "rerun":
		run, err = client.RerunTriggerRun(callCtx, &corev1.RerunTriggerRunRequest{RunId: strings.TrimSpace(runID)})
	default:
		run, err = client.CancelTriggerRun(callCtx, &corev1.CancelTriggerRunRequest{RunId: strings.TrimSpace(runID)})
	}
	if err != nil {
		return triggerError(err)
	}
	verb := "queued"
	if action == "cancel" {
		verb = "canceled"
	}
	return r.printTriggerRun(opts, verb, run)
}

func (r Runner) printTriggerRun(opts commandOptions, verb string, run *corev1.TriggerRun) error {
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, triggerRunToOutput(run))
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "%s run %s of %s\n", verb, run.Id, run.TriggerId)
	}
	return nil
}

// fullChangesetID turns a changeset handle into its id.
func fullChangesetID(ctx context.Context, conn *grpc.ClientConn, handle string) (string, error) {
	cs, err := corev1.NewChangesetServiceClient(conn).GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: strings.TrimSpace(handle)})
	if err != nil {
		return "", triggerError(err)
	}
	return cs.Id, nil
}

func triggerError(err error) error {
	message := grpcstatus.Convert(err).Message()
	switch grpcstatus.Code(err) {
	case codes.InvalidArgument:
		return userError("invalid_argument", message, "")
	case codes.FailedPrecondition:
		return userError("failed_precondition", message, "")
	case codes.PermissionDenied:
		return userError("permission_denied", message, "")
	case codes.NotFound:
		return userError("not_found", message, "")
	case codes.Unavailable, codes.Unimplemented:
		return userError("unsupported", "this server does not run triggers", "Upgrade the server.")
	}
	return err
}
