package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

// checkCommand builds gs check: list a changeset's check runs, print a run's
// log, rerun one, or allow secrets on a contributor's patchset. A check is
// named by its run id (run_...), or by its name (full, such as gitslice/e2e,
// or the part after the last /) on the workspace's changeset or the one given
// with --changeset.
func (r Runner) checkCommand(opts *commandOptions) *cobra.Command {
	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "List a changeset's check runs, print a check's log, rerun a check, allow secrets",
		RunE:  requireSubcommand("check"),
	}
	listChangeset := ""
	listCmd := &cobra.Command{
		Use:   "list [changeset]",
		Short: "List the latest run of each check on a changeset's current patchset",
		Args:  maxArgs(1, "gs check list [changeset]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				listChangeset = args[0]
			}
			return r.runCheckList(cmd.Context(), *opts, listChangeset)
		},
	}
	logsChangeset := ""
	logsFollow := false
	logsTail := 0
	logsCmd := &cobra.Command{
		Use:   "logs <check|run-id>",
		Short: "Print a check run's log (--follow streams it until the run ends)",
		Args:  exactArgs(1, "gs check logs <check|run-id> [--changeset c] [--follow] [--tail n]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runCheckLogs(cmd.Context(), *opts, logsChangeset, args[0], logsFollow, logsTail)
		},
	}
	logsCmd.Flags().StringVar(&logsChangeset, "changeset", "", "changeset whose check to read (default: the workspace's)")
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "keep streaming until the run ends")
	logsCmd.Flags().IntVar(&logsTail, "tail", 0, "print only the last n lines")
	rerunChangeset := ""
	rerunCmd := &cobra.Command{
		Use:   "rerun <check|run-id>",
		Short: "Run a check again on the changeset's current patchset",
		Args:  exactArgs(1, "gs check rerun <check|run-id> [--changeset c]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runCheckRerun(cmd.Context(), *opts, rerunChangeset, args[0])
		},
	}
	rerunCmd.Flags().StringVar(&rerunChangeset, "changeset", "", "changeset whose check to rerun (default: the workspace's)")
	allowPatchset := ""
	allowCmd := &cobra.Command{
		Use:   "allow [changeset]",
		Short: "Allow a contributor patchset's CI checks to run with secrets and rerun them",
		Args:  maxArgs(1, "gs check allow [changeset] [--patchset id]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			requested := ""
			if len(args) > 0 {
				requested = args[0]
			}
			return r.runCheckAllow(cmd.Context(), *opts, requested, allowPatchset)
		},
	}
	allowCmd.Flags().StringVar(&allowPatchset, "patchset", "", "patchset id to allow secrets on (default: current patchset)")
	checkCmd.AddCommand(listCmd, logsCmd, rerunCmd, allowCmd)
	return checkCmd
}

type allowPatchsetSecretsOutput struct {
	Changeset   string           `json:"changeset"`
	ChangesetID string           `json:"changeset_id"`
	PatchsetID  string           `json:"patchset_id"`
	AllowedBy   string           `json:"allowed_by"`
	AllowedAt   string           `json:"allowed_at,omitempty"`
	RerunChecks []checkRunOutput `json:"rerun_checks,omitempty"`
}

type checkRunOutput struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	RunID      string `json:"run_id"`
	Attempt    int32  `json:"attempt"`
	Summary    string `json:"summary,omitempty"`
	Daemon     string `json:"daemon,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// latestCheckRuns is the latest attempt of each check on the changeset's
// current patchset, by name.
func (r Runner) latestCheckRuns(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, requested string) (*corev1.Changeset, []*corev1.CheckRun, error) {
	_, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requested)
	if err != nil {
		return nil, nil, err
	}
	cs, err := corev1.NewChangesetServiceClient(conn).GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
	if err != nil {
		return nil, nil, err
	}
	patchsetID := cs.GetCurrentPatchsetId()
	res, err := corev1.NewCheckServiceClient(conn).ListCheckRuns(authContext(ctx, cfg), &corev1.ListCheckRunsRequest{ChangesetId: cs.GetId(), PatchsetId: patchsetID})
	if err != nil {
		return nil, nil, err
	}
	latest := map[string]*corev1.CheckRun{}
	for _, run := range res.GetRuns() {
		if patchsetID != "" && run.GetPatchsetId() != patchsetID {
			continue
		}
		if prev := latest[run.GetCheckName()]; prev == nil || run.GetAttempt() > prev.GetAttempt() || (run.GetAttempt() == prev.GetAttempt() && run.GetCreatedAt() > prev.GetCreatedAt()) {
			latest[run.GetCheckName()] = run
		}
	}
	runs := make([]*corev1.CheckRun, 0, len(latest))
	for _, run := range latest {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].GetCheckName() < runs[j].GetCheckName() })
	return cs, runs, nil
}

func (r Runner) runCheckList(ctx context.Context, opts commandOptions, requested string) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	cs, runs, err := r.latestCheckRuns(ctx, conn, cfg, requested)
	if err != nil {
		return err
	}
	out := make([]checkRunOutput, 0, len(runs))
	for _, run := range runs {
		out = append(out, checkRunOutput{Name: run.GetCheckName(), Status: run.GetStatus(), RunID: run.GetId(), Attempt: run.GetAttempt(), Summary: run.GetSummary(), Daemon: run.GetDaemonName(), DurationMs: run.GetDurationMs()})
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"changeset": firstNonEmpty(displayChangesetID(cs), cs.GetId()), "checks": out})
	}
	if opts.Quiet {
		return nil
	}
	if len(out) == 0 {
		fmt.Fprintf(r.Stdout, "no check runs on %s's current patchset\n", firstNonEmpty(displayChangesetID(cs), cs.GetId()))
		return nil
	}
	for _, c := range out {
		line := fmt.Sprintf("%-10s %-24s %s", c.Status, c.Name, c.RunID)
		if c.Summary != "" && c.Status != "passed" {
			line += "  " + c.Summary
		}
		fmt.Fprintln(r.Stdout, line)
	}
	return nil
}

// resolveCheckRun finds the run a gs check argument names.
func (r Runner) resolveCheckRun(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, changeset, target string) (*corev1.CheckRun, error) {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "run_") {
		return corev1.NewCheckServiceClient(conn).GetCheckRun(authContext(ctx, cfg), &corev1.GetCheckRunRequest{RunId: target})
	}
	_, runs, err := r.latestCheckRuns(ctx, conn, cfg, changeset)
	if err != nil {
		return nil, err
	}
	var matches []*corev1.CheckRun
	for _, run := range runs {
		name := run.GetCheckName()
		if name == target || name[strings.LastIndex(name, "/")+1:] == target {
			matches = append(matches, run)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		names := make([]string, 0, len(runs))
		for _, run := range runs {
			names = append(names, run.GetCheckName())
		}
		return nil, userError("check_not_found", "no check "+target+" on the changeset's current patchset", "Checks there: "+firstNonEmpty(strings.Join(names, ", "), "none")+". gs check list shows them.")
	default:
		return nil, userError("ambiguous_check", "more than one check is called "+target, "Use the full name or the run id from gs check list.")
	}
}

func (r Runner) runCheckLogs(ctx context.Context, opts commandOptions, changeset, target string, follow bool, tail int) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	run, err := r.resolveCheckRun(ctx, conn, cfg, changeset, target)
	if err != nil {
		return err
	}
	if tail > 0 {
		text, err := r.checkRunLog(ctx, conn, cfg, run, follow)
		if err != nil {
			return err
		}
		_, err = io.WriteString(r.Stdout, tailLines(text, tail))
		return err
	}
	return r.streamCheckRunLog(ctx, conn, cfg, run, follow, r.Stdout)
}

// checkRunLog reads a run's whole log (until it ends when follow is set).
func (r Runner) checkRunLog(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, run *corev1.CheckRun, follow bool) (string, error) {
	var b strings.Builder
	err := r.streamCheckRunLog(ctx, conn, cfg, run, follow, &b)
	return b.String(), err
}

// streamCheckRunLog writes a run's log to w. The server follows a run that
// has not ended; without follow, reading stops once the stored log has been
// sent (nothing new for a second).
func (r Runner) streamCheckRunLog(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, run *corev1.CheckRun, follow bool, w io.Writer) error {
	streamCtx, cancel := context.WithCancel(authContext(ctx, cfg))
	defer cancel()
	stream, err := corev1.NewCheckServiceClient(conn).StreamCheckRun(streamCtx, &corev1.StreamCheckRunRequest{RunId: run.GetId()})
	if err != nil {
		return err
	}
	terminal := isTerminalCheckStatus(run.GetStatus())
	type recvResult struct {
		log *corev1.CheckRunLog
		err error
	}
	results := make(chan recvResult, 1)
	go func() {
		for {
			log, err := stream.Recv()
			results <- recvResult{log, err}
			if err != nil {
				return
			}
		}
	}()
	for {
		var res recvResult
		if follow || terminal {
			res = <-results
		} else {
			select {
			case res = <-results:
			case <-time.After(time.Second):
				return nil
			}
		}
		if errors.Is(res.err, io.EOF) {
			return nil
		}
		if res.err != nil {
			return res.err
		}
		if _, err := io.WriteString(w, res.log.GetChunk()); err != nil {
			return err
		}
	}
}

func isTerminalCheckStatus(status string) bool {
	switch status {
	case "passed", "failed", "errored", "cancelled", "canceled", "skipped":
		return true
	}
	return false
}

func tailLines(text string, n int) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

func (r Runner) runCheckRerun(ctx context.Context, opts commandOptions, changeset, target string) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	run, err := r.resolveCheckRun(ctx, conn, cfg, changeset, target)
	if err != nil {
		return err
	}
	next, err := corev1.NewCheckServiceClient(conn).RerunCheck(authContext(ctx, cfg), &corev1.RerunCheckRequest{RunId: run.GetId()})
	if err != nil {
		return err
	}
	out := checkRunOutput{Name: next.GetCheckName(), Status: next.GetStatus(), RunID: next.GetId(), Attempt: next.GetAttempt()}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "rerunning %s as %s (attempt %d); gs check logs %s --follow to watch\n", out.Name, out.RunID, out.Attempt, out.RunID)
	}
	return nil
}

func (r Runner) runCheckAllow(ctx context.Context, opts commandOptions, requested, patchsetID string) error {
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requested)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewCheckServiceClient(conn).AllowPatchsetSecrets(authContext(ctx, cfg), &corev1.AllowPatchsetSecretsRequest{
		ChangesetId: changesetID,
		PatchsetId:  strings.TrimSpace(patchsetID),
	})
	if err != nil {
		return err
	}
	runs := make([]checkRunOutput, 0, len(res.GetRerunChecks()))
	for _, run := range res.GetRerunChecks() {
		runs = append(runs, checkRunOutput{
			Name:       run.GetCheckName(),
			Status:     run.GetStatus(),
			RunID:      run.GetId(),
			Attempt:    run.GetAttempt(),
			Summary:    run.GetSummary(),
			Daemon:     run.GetDaemonName(),
			DurationMs: run.GetDurationMs(),
		})
	}
	csLabel := firstNonEmpty(storage.ShortChangesetID(res.GetChangesetId()), res.GetChangesetId(), changesetID)
	out := allowPatchsetSecretsOutput{
		Changeset:   csLabel,
		ChangesetID: res.GetChangesetId(),
		PatchsetID:  res.GetPatchsetId(),
		AllowedBy:   res.GetAllowedBySubjectId(),
		AllowedAt:   res.GetAllowedAt(),
		RerunChecks: runs,
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if len(runs) == 0 {
		fmt.Fprintf(r.Stdout, "allowed secrets for %s (%s)\n", csLabel, out.PatchsetID)
		return nil
	}
	fmt.Fprintf(r.Stdout, "allowed secrets for %s (%s); running %d check(s):\n", csLabel, out.PatchsetID, len(runs))
	for _, c := range runs {
		fmt.Fprintf(r.Stdout, "  %-10s %-24s %s\n", c.Status, c.Name, c.RunID)
	}
	return nil
}

var failingCheckPattern = regexp.MustCompile(`required check "([^"]+)" is failing`)

// reportFailingChecks prints the end of each failing required check's log
// after a blocked submit, so the reason is on screen without another command.
func (r Runner) reportFailingChecks(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, opts commandOptions, res *corev1.SubmitStackResponse) {
	if res == nil || opts.Quiet || opts.jsonOutput() {
		return
	}
	for _, result := range res.GetResults() {
		match := failingCheckPattern.FindStringSubmatch(result.GetBlockedReason())
		if match == nil {
			continue
		}
		run, err := r.resolveCheckRun(ctx, conn, cfg, result.GetChangesetId(), match[1])
		if err != nil {
			continue
		}
		text, err := r.checkRunLog(ctx, conn, cfg, run, false)
		if err != nil || strings.TrimSpace(text) == "" {
			continue
		}
		fmt.Fprintf(r.stderr(), "--- last lines of %s (%s); full log: gs check logs %s ---\n", run.GetCheckName(), run.GetId(), run.GetId())
		fmt.Fprint(r.stderr(), tailLines(text, 30))
	}
}
