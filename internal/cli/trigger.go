package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

// triggerCommand shows folder automations and their runs, and runs manual
// automations by hand (design/34_triggers.md).
func (r Runner) triggerCommand(opts *commandOptions) *cobra.Command {
	autoCmd := &cobra.Command{
		Use:     "automation",
		Aliases: []string{"auto", "automations"},
		Short:   "Show folder automations and their runs; run manual automations",
		Long: "An automation is defined in <folder>/.gitslice/automations.yaml and runs on folder events or manually:\n" +
			"an HTTP call, a JS worker on Cloudflare, a bash container on Cloudflare, or folder checks. See gs help automations.",
		RunE: requireSubcommand("automation"),
	}
	markSchemaGroup(autoCmd)

	var lsRef string
	lsCmd := &cobra.Command{
		Use:     "ls [folder]",
		Aliases: []string{"list"},
		Short:   "List the automations that apply at a folder (default: the current one)",
		Args:    maxArgs(1, "gs automation ls [folder] [--ref <ref>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			folder := ""
			if len(args) == 1 {
				folder = args[0]
			}
			return r.runTriggerList(cmd.Context(), *opts, folder, lsRef)
		},
	}
	lsCmd.Flags().StringVar(&lsRef, "ref", "", "read the definitions at this ref (default refs/global/main)")

	var runsAccount, runsAuto, runsChangeset string
	var runsLimit int
	runsCmd := &cobra.Command{
		Use:   "runs",
		Short: "List automation runs of a changeset, an automation or an account, newest first",
		Args:  noArgs("gs automation runs [--changeset <id>] [--automation <id>] [--account <account>] [--limit n]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerRuns(cmd.Context(), *opts, runsAccount, runsAuto, runsChangeset, runsLimit)
		},
	}
	runsCmd.Flags().StringVar(&runsChangeset, "changeset", "", "runs about this changeset")
	runsCmd.Flags().StringVar(&runsAuto, "automation", "", "runs of this automation id, such as services/billing/deploy")
	runsCmd.Flags().StringVar(&runsAccount, "account", "", "the account (default: the workspace's account, else your active account)")
	runsCmd.Flags().IntVar(&runsLimit, "limit", 20, "how many runs")

	showCmd := &cobra.Command{
		Use:   "show <run-id>",
		Short: "Show an automation run",
		Args:  exactArgs(1, "gs automation show <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerShow(cmd.Context(), *opts, args[0])
		},
	}

	var logsFollow bool
	logsCmd := &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Print an automation run's log",
		Args:  exactArgs(1, "gs automation logs <run-id> [--follow]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerLogs(cmd.Context(), *opts, args[0], logsFollow)
		},
	}
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "stream logs until the run finishes")

	var runAccount, runRef, runChangeset string
	var runInputs []string
	var runYes, runFollow, runLocal bool
	var runFromChangeset string
	var runSecrets, runCallbacks []string
	runCmd := &cobra.Command{
		Use:   "run <automation-id>",
		Short: "Run an automation that listens for manual",
		Long: `Run an automation that listens for manual.

Before an automation lands, try it in one of two ways:

  --from-changeset <id>  run it on the server as defined in that open changeset
                         (an automation that lists secrets needs an account admin)
  --local                run a worker automation's script here with node, from
                         the workspace's files, with a stand-in gs; secrets come
                         from --secret NAME=value and callbacks from --callback`,
		Args: exactArgs(1, "gs automation run <automation-id> [--account <account>] [--ref <ref>] [--changeset <id> | --from-changeset <id>] [-i key=value]... [--yes] [--follow] [--local [--secret NAME=value]... [--callback body.json]...]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runLocal {
				if runFromChangeset != "" || runChangeset != "" || runRef != "" {
					return userError("invalid_argument", "--local runs the workspace's files; it takes no --ref, --changeset or --from-changeset", "")
				}
				return r.runLocalAutomation(cmd.Context(), *opts, runAccount, args[0], runInputs, runSecrets, runCallbacks)
			}
			if len(runSecrets) > 0 || len(runCallbacks) > 0 {
				return userError("invalid_argument", "--secret and --callback go with --local", "")
			}
			if runFromChangeset != "" {
				if runChangeset != "" && runChangeset != runFromChangeset {
					return userError("invalid_argument", "--from-changeset already sets the changeset the run is about", "")
				}
				if runRef != "" {
					return userError("invalid_argument", "--from-changeset takes no --ref", "")
				}
			}
			return r.runTriggerRun(cmd.Context(), *opts, runAccount, args[0], runRef, runChangeset, runFromChangeset, runInputs, runYes, runFollow)
		},
	}
	runCmd.Flags().StringVar(&runAccount, "account", "", "the account (default: the workspace's account, else your active account)")
	runCmd.Flags().StringVar(&runRef, "ref", "", "run the definition at this ref (default refs/global/main)")
	runCmd.Flags().StringVar(&runChangeset, "changeset", "", "a changeset the run is about; its checks may be reported")
	runCmd.Flags().StringArrayVarP(&runInputs, "input", "i", nil, "key=value passed as automation input (repeatable)")
	runCmd.Flags().BoolVarP(&runYes, "yes", "y", false, "confirm manual.confirm prompt without asking")
	runCmd.Flags().BoolVarP(&runFollow, "follow", "f", false, "stream logs until the run finishes and exit non-zero on failure")
	runCmd.Flags().StringVar(&runFromChangeset, "from-changeset", "", "run the automation as defined in this open changeset, before it lands")
	runCmd.Flags().BoolVar(&runLocal, "local", false, "run a worker automation's script locally with node, from the workspace's files")
	runCmd.Flags().StringArrayVar(&runSecrets, "secret", nil, "with --local: NAME=value for a secret the automation lists (repeatable)")
	runCmd.Flags().StringArrayVar(&runCallbacks, "callback", nil, "with --local: a file whose contents are POSTed to onCallback after onEvent (repeatable, in order)")

	rerunCmd := &cobra.Command{
		Use:   "rerun <run-id>",
		Short: "Run a finished automation run again for the same event",
		Args:  exactArgs(1, "gs automation rerun <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerRunAction(cmd.Context(), *opts, args[0], "rerun")
		},
	}
	cancelCmd := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel an automation run that has not finished",
		Args:  exactArgs(1, "gs automation cancel <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runTriggerRunAction(cmd.Context(), *opts, args[0], "cancel")
		},
	}

	autoCmd.AddCommand(lsCmd, runsCmd, showCmd, logsCmd, runCmd, rerunCmd, cancelCmd)
	return autoCmd
}

type automationInputOutput struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
}

type automationManualOutput struct {
	Roles        []string `json:"roles,omitempty"`
	RequireHuman bool     `json:"require_human,omitempty"`
	Confirm      string   `json:"confirm,omitempty"`
}

type automationBashOutput struct {
	Runtime    string   `json:"runtime,omitempty"`
	Size       string   `json:"size,omitempty"`
	Checkout   string   `json:"checkout,omitempty"`
	Script     string   `json:"script,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
	Include    []string `json:"include,omitempty"`
}

type triggerOutput struct {
	ID          string                  `json:"id"`
	Folder      string                  `json:"folder"`
	Description string                  `json:"description,omitempty"`
	On          []string                `json:"on"`
	Paths       []string                `json:"paths,omitempty"`
	Changes     []string                `json:"changes,omitempty"`
	Action      string                  `json:"action"`
	Worker      string                  `json:"worker,omitempty"`
	URL         string                  `json:"url,omitempty"`
	Bash        *automationBashOutput   `json:"bash,omitempty"`
	Egress      string                  `json:"egress,omitempty"`
	Checks      []string                `json:"checks,omitempty"`
	Secrets     []string                `json:"secrets,omitempty"`
	Reveal      []string                `json:"reveal,omitempty"`
	Network     []string                `json:"network,omitempty"`
	Inputs      []automationInputOutput `json:"inputs,omitempty"`
	Manual      *automationManualOutput `json:"manual,omitempty"`
	Concurrency string                  `json:"concurrency"`
}

func triggerDefToOutput(t *corev1.TriggerDefinition) triggerOutput {
	out := triggerOutput{
		ID: t.GetId(), Folder: t.GetFolder(), Description: t.GetDescription(),
		On: append([]string(nil), t.GetOn()...), Paths: append([]string(nil), t.GetPaths()...),
		Changes: append([]string(nil), t.GetChanges()...), Action: t.GetAction(),
		Worker: t.GetWorker(), URL: t.GetHttpUrl(), Egress: t.GetEgress(),
		Checks: append([]string(nil), t.GetChecks()...), Secrets: append([]string(nil), t.GetSecrets()...),
		Reveal: append([]string(nil), t.GetReveal()...), Network: append([]string(nil), t.GetNetwork()...),
		Concurrency: t.GetConcurrency(),
	}
	if b := t.GetBash(); b != nil {
		out.Bash = &automationBashOutput{
			Runtime: b.GetRuntime(), Size: b.GetSize(), Checkout: b.GetCheckout(),
			Script: b.GetScript(), WorkingDir: b.GetWorkingDir(), Include: append([]string(nil), b.GetInclude()...),
		}
	}
	if m := t.GetManual(); m != nil {
		out.Manual = &automationManualOutput{
			Roles: append([]string(nil), m.GetRoles()...), RequireHuman: m.GetRequireHuman(), Confirm: m.GetConfirm(),
		}
	}
	for _, in := range t.GetInputs() {
		out.Inputs = append(out.Inputs, automationInputOutput{
			Name: in.GetName(), Type: in.GetType(), Description: in.GetDescription(),
			Required: in.GetRequired(), Default: in.GetDefaultValue(), Options: append([]string(nil), in.GetOptions()...),
			Pattern: in.GetPattern(),
		})
	}
	return out
}

func automationActionSummary(t triggerOutput) string {
	switch t.Action {
	case "http":
		return t.URL
	case "check":
		return strings.Join(t.Checks, ",")
	case "bash":
		parts := []string{}
		if t.Bash != nil && t.Bash.Runtime != "" {
			parts = append(parts, t.Bash.Runtime)
		}
		if t.Egress != "" {
			parts = append(parts, t.Egress)
		}
		if t.Bash != nil && t.Bash.Script != "" {
			parts = append(parts, t.Bash.Script)
		}
		return strings.Join(parts, " ")
	default:
		return t.Worker
	}
}

type triggerRunOutput struct {
	ID              string            `json:"id"`
	Automation      string            `json:"automation"`
	Status          string            `json:"status"`
	Event           string            `json:"event"`
	EventID         string            `json:"event_id"`
	Action          string            `json:"action"`
	TriggeredBy     string            `json:"triggered_by,omitempty"`
	TriggeredByKind string            `json:"triggered_by_kind,omitempty"`
	Inputs          map[string]string `json:"inputs,omitempty"`
	Changeset       string            `json:"changeset,omitempty"`
	Attempt         int32             `json:"attempt"`
	MaxAttempts     int32             `json:"max_attempts"`
	Error           string            `json:"error,omitempty"`
	CreatedAt       string            `json:"created_at"`
	FinishedAt      string            `json:"finished_at,omitempty"`
	Definition      string            `json:"definition_commit,omitempty"`
	DetailsURL      string            `json:"details_url,omitempty"`
}

func triggerRunToOutput(run *corev1.TriggerRun) triggerRunOutput {
	return triggerRunOutput{
		ID: run.Id, Automation: run.TriggerId, Status: run.Status, Event: run.EventKind, EventID: run.EventId, Action: run.Action,
		TriggeredBy: run.TriggeredBy, TriggeredByKind: run.TriggeredByKind, Inputs: run.Inputs,
		Changeset: model.ShortChangesetID(run.ChangesetId), Attempt: run.Attempt, MaxAttempts: run.MaxAttempts,
		Error: run.Error, CreatedAt: run.CreatedAt, FinishedAt: run.FinishedAt, Definition: run.DefinitionCommitId,
		DetailsURL: run.DetailsUrl,
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

// automationFolder resolves a folder for automation commands: canonical path,
// current workspace subfolder, or — when invoked from the workspace root of a
// single-path slice — that slice's included path.
func (r Runner) automationFolder(raw string) (string, error) {
	folder, err := r.triggerFolder(raw)
	if err == nil {
		return folder, nil
	}
	if strings.TrimSpace(raw) == "" {
		if ws, werr := r.readWorkspaceConfig(); werr == nil && len(ws.IncludedPaths) == 1 {
			if canonical, perr := paths.CanonicalPrefix(ws.IncludedPaths[0]); perr == nil {
				return canonical, nil
			}
		}
	}
	return "", err
}

func (r Runner) runTriggerList(ctx context.Context, opts commandOptions, rawFolder, ref string) error {
	folder, err := r.automationFolder(rawFolder)
	if err != nil {
		return err
	}
	var resp *corev1.ListTriggersResponse
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		resp, err = s.Trigger().ListTriggers(s.Ctx(), &corev1.ListTriggersRequest{Path: folder, Ref: ref})
		return triggerError(err)
	}); err != nil {
		return err
	}
	out := make([]triggerOutput, 0, len(resp.Triggers))
	for _, t := range resp.Triggers {
		out = append(out, triggerDefToOutput(t))
	}
	if opts.jsonOutput() {
		problems := make([]map[string]string, 0, len(resp.Problems))
		for _, p := range resp.Problems {
			problems = append(problems, map[string]string{"file": p.File, "message": p.Message})
		}
		return r.writeJSONOutput(opts, map[string]any{"folder": folder, "commit": resp.CommitId, "automations": out, "problems": problems})
	}
	if opts.Quiet {
		return nil
	}
	if len(out) == 0 && len(resp.Problems) == 0 {
		fmt.Fprintf(r.Stdout, "no automations apply at %s\n", folder)
		return nil
	}
	for _, t := range out {
		line := fmt.Sprintf("%s\ton %s\t%s %s", t.ID, strings.Join(t.On, ","), t.Action, automationActionSummary(t))
		if len(t.Paths) > 0 {
			line += "\tpaths " + strings.Join(t.Paths, ",")
		}
		if t.Action != "check" && len(t.Checks) > 0 {
			line += "\tchecks " + strings.Join(t.Checks, ",")
		}
		fmt.Fprintln(r.Stdout, line)
	}
	for _, p := range resp.Problems {
		fmt.Fprintf(r.stderr(), "%s: %s\n", p.File, p.Message)
	}
	return nil
}

func (r Runner) runTriggerRuns(ctx context.Context, opts commandOptions, account, automation, changeset string, limit int) error {
	var resp *corev1.ListTriggerRunsResponse
	if err := r.withSession(ctx, func(s *Session) error {
		req := &corev1.ListTriggerRunsRequest{TriggerId: strings.TrimSpace(automation), Limit: int32(limit)}
		if changeset != "" {
			id, err := s.fullTriggerChangesetID(changeset)
			if err != nil {
				return err
			}
			req.ChangesetId = id
		} else {
			var err error
			if req.Account, err = r.resolveAutomationAccount(s, account); err != nil {
				return err
			}
			if req.TriggerId != "" && !strings.Contains(req.TriggerId, "/") {
				resolvedID, _ := r.resolveAutomationIDAndDef(s, req.Account, req.TriggerId, "", false)
				req.TriggerId = resolvedID
			}
		}
		var err error
		resp, err = s.Trigger().ListTriggerRuns(s.Ctx(), req)
		return triggerError(err)
	}); err != nil {
		return err
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
		fmt.Fprintln(r.Stdout, "no automation runs")
		return nil
	}
	for _, run := range out {
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", run.ID, run.Status, run.Automation, run.Event, run.CreatedAt)
		if run.TriggeredBy != "" {
			line += "\tby " + run.TriggeredBy
		}
		if run.Error != "" {
			line += "\t" + oneLineSummary(run.Error)
		}
		fmt.Fprintln(r.Stdout, line)
	}
	return nil
}

func (r Runner) runTriggerShow(ctx context.Context, opts commandOptions, runID string) error {
	var run *corev1.TriggerRun
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		run, err = s.Trigger().GetTriggerRun(s.Ctx(), &corev1.GetTriggerRunRequest{RunId: strings.TrimSpace(runID)})
		return triggerError(err)
	}); err != nil {
		return err
	}
	out := triggerRunToOutput(run)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "run: %s\nautomation: %s (%s)\nstatus: %s\nevent: %s %s\nattempt: %d of %d\ncreated: %s\n",
		out.ID, out.Automation, out.Action, out.Status, out.Event, out.EventID, out.Attempt, out.MaxAttempts, out.CreatedAt)
	if out.TriggeredBy != "" {
		actor := out.TriggeredBy
		if out.TriggeredByKind != "" {
			actor += " (" + out.TriggeredByKind + ")"
		}
		fmt.Fprintf(r.Stdout, "triggered_by: %s\n", actor)
	}
	if len(out.Inputs) > 0 {
		keys := make([]string, 0, len(out.Inputs))
		for k := range out.Inputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+out.Inputs[k])
		}
		fmt.Fprintf(r.Stdout, "inputs: %s\n", strings.Join(parts, " "))
	}
	if out.Changeset != "" {
		fmt.Fprintf(r.Stdout, "changeset: %s\n", out.Changeset)
	}
	if out.FinishedAt != "" {
		fmt.Fprintf(r.Stdout, "finished: %s\n", out.FinishedAt)
	}
	if out.DetailsURL != "" {
		fmt.Fprintf(r.Stdout, "details: %s\n", out.DetailsURL)
	}
	if out.Error != "" {
		fmt.Fprintf(r.Stdout, "error: %s\n", out.Error)
	}
	return nil
}

func (r Runner) runTriggerLogs(ctx context.Context, opts commandOptions, runID string, follow bool) error {
	if follow && !opts.jsonOutput() {
		_, err := r.followAutomationRun(ctx, runID)
		return err
	}
	var all []*corev1.TriggerRunLog
	if err := r.withSession(ctx, func(s *Session) error {
		var after int64
		for {
			resp, err := s.Trigger().ListTriggerRunLogs(s.Ctx(), &corev1.ListTriggerRunLogsRequest{RunId: strings.TrimSpace(runID), AfterSeq: after})
			if err != nil {
				return triggerError(err)
			}
			if len(resp.Logs) == 0 {
				return nil
			}
			all = append(all, resp.Logs...)
			after = resp.Logs[len(resp.Logs)-1].Seq
		}
	}); err != nil {
		return err
	}
	if opts.jsonOutput() {
		lines := make([]map[string]any, 0, len(all))
		for _, l := range all {
			lines = append(lines, map[string]any{"seq": l.Seq, "stream": l.Stream, "chunk": l.Chunk, "created_at": l.CreatedAt})
		}
		return r.writeJSONOutput(opts, map[string]any{"logs": lines})
	}
	for _, l := range all {
		r.printRunLogChunk(l)
	}
	return nil
}

func (r Runner) printRunLogChunk(l *corev1.TriggerRunLog) {
	if l.Stream == "stderr" {
		fmt.Fprint(r.stderr(), l.Chunk)
	} else {
		fmt.Fprint(r.Stdout, l.Chunk)
	}
}

func (r Runner) followAutomationRun(ctx context.Context, runID string) (*corev1.TriggerRun, error) {
	runID = strings.TrimSpace(runID)
	var after int64
	var finalRun *corev1.TriggerRun
	shownDetails := ""
	err := r.withSession(ctx, func(s *Session) error {
		for {
			resp, err := s.Trigger().ListTriggerRunLogs(s.Ctx(), &corev1.ListTriggerRunLogsRequest{RunId: runID, AfterSeq: after})
			if err != nil {
				return triggerError(err)
			}
			for _, l := range resp.Logs {
				r.printRunLogChunk(l)
				after = l.Seq
			}
			run, err := s.Trigger().GetTriggerRun(s.Ctx(), &corev1.GetTriggerRunRequest{RunId: runID})
			if err != nil {
				return triggerError(err)
			}
			finalRun = run
			if run.DetailsUrl != "" && run.DetailsUrl != shownDetails {
				shownDetails = run.DetailsUrl
				fmt.Fprintf(r.stderr(), "details: %s\n", run.DetailsUrl)
			}
			if automationRunFinal(run.Status) {
				// Drain any final lines emitted right before status flipped.
				tail, terr := s.Trigger().ListTriggerRunLogs(s.Ctx(), &corev1.ListTriggerRunLogsRequest{RunId: runID, AfterSeq: after})
				if terr == nil {
					for _, l := range tail.Logs {
						r.printRunLogChunk(l)
						after = l.Seq
					}
				}
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1500 * time.Millisecond):
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if finalRun != nil && finalRun.Status != "succeeded" {
		msg := fmt.Sprintf("automation run %s ended with status %s", finalRun.Id, finalRun.Status)
		if finalRun.Error != "" {
			msg += ": " + finalRun.Error
		}
		return finalRun, userError("automation_failed", msg, "")
	}
	return finalRun, nil
}

func (r Runner) runTriggerRun(ctx context.Context, opts commandOptions, account, triggerID, ref, changeset, fromChangeset string, rawInputs []string, yes, follow bool) error {
	inputs := map[string]string{}
	for _, raw := range rawInputs {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return userError("invalid_argument", "--input "+raw+" is not key=value", "")
		}
		inputs[strings.TrimSpace(key)] = value
	}
	triggerID = strings.TrimSpace(triggerID)
	var run *corev1.TriggerRun
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		if account, err = r.resolveAutomationAccount(s, account); err != nil {
			return err
		}
		resolvedID, def := r.resolveAutomationIDAndDef(s, account, triggerID, ref, fromChangeset != "")
		triggerID = resolvedID
		if fromChangeset == "" {
			// The landed definition's prompts; a changeset's definition is
			// checked by the server alone.
			if def != nil {
				if err := r.promptMissingInputsAndConfirm(opts, def, inputs, yes); err != nil {
					return err
				}
			}
		} else {
			changeset = fromChangeset
		}
		req := &corev1.RunTriggerRequest{Account: account, TriggerId: triggerID, Ref: ref, Inputs: inputs, DefinitionFromChangeset: fromChangeset != ""}
		if changeset != "" {
			if req.ChangesetId, err = s.fullTriggerChangesetID(changeset); err != nil {
				return err
			}
		}
		run, err = s.Trigger().RunTrigger(s.Ctx(), req)
		return triggerError(err)
	}); err != nil {
		return err
	}
	if follow && !opts.jsonOutput() {
		if !opts.Quiet {
			fmt.Fprintf(r.stderr(), "started run %s of %s\n", run.Id, run.TriggerId)
		}
		_, err := r.followAutomationRun(ctx, run.Id)
		return err
	}
	return r.printTriggerRun(opts, "queued", run)
}

func automationRunFinal(status string) bool {
	switch status {
	case "succeeded", "failed", "errored", "canceled", "timed_out":
		return true
	}
	return false
}

func automationFolderOf(account, automationID string) string {
	id := strings.Trim(strings.TrimSpace(automationID), "/")
	root := "/" + strings.Trim(account, "/")
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return root + "/" + id[:i]
	}
	return root
}

func (r Runner) resolveAutomationAccount(s *Session, raw string) (string, error) {
	if strings.TrimSpace(raw) != "" {
		return s.ResolveAccount(raw)
	}
	if ws, err := r.readWorkspaceConfig(); err == nil && strings.TrimSpace(ws.Account) != "" {
		return normalizeCLISlug(ws.Account, "account")
	}
	return s.ResolveAccount("")
}

func (r Runner) resolveWorkspaceAutomationFolder(account string) string {
	folder, err := r.automationFolder("")
	if err != nil {
		return ""
	}
	prefix := "/" + strings.Trim(account, "/")
	if folder == prefix || strings.HasPrefix(folder, prefix+"/") {
		return folder
	}
	return ""
}

func (r Runner) resolveAutomationIDAndDef(s *Session, account, triggerID, ref string, fromChangeset bool) (string, *corev1.TriggerDefinition) {
	triggerID = strings.TrimSpace(triggerID)
	if strings.Contains(triggerID, "/") {
		if fromChangeset {
			return triggerID, nil
		}
		return triggerID, lookupAutomationDef(s, account, triggerID, ref)
	}
	folder := r.resolveWorkspaceAutomationFolder(account)
	if folder == "" {
		if fromChangeset {
			return triggerID, nil
		}
		return triggerID, lookupAutomationDef(s, account, triggerID, ref)
	}
	if !fromChangeset {
		resp, err := s.Trigger().ListTriggers(s.Ctx(), &corev1.ListTriggersRequest{Path: folder, Ref: ref})
		if err == nil {
			var best *corev1.TriggerDefinition
			for _, def := range resp.GetTriggers() {
				if def.GetId() == triggerID || def.GetName() == triggerID {
					if best == nil || len(def.GetFolder()) > len(best.GetFolder()) {
						best = def
					}
				}
			}
			if best != nil {
				return best.GetId(), best
			}
		}
		return triggerID, nil
	}
	rel := strings.TrimPrefix(folder, "/"+strings.Trim(account, "/"))
	rel = strings.Trim(rel, "/")
	if rel != "" {
		return rel + "/" + triggerID, nil
	}
	return triggerID, nil
}

func lookupAutomationDef(s *Session, account, automationID, ref string) *corev1.TriggerDefinition {
	folder := automationFolderOf(account, automationID)
	resp, err := s.Trigger().ListTriggers(s.Ctx(), &corev1.ListTriggersRequest{Path: folder, Ref: ref})
	if err != nil {
		return nil
	}
	for _, def := range resp.GetTriggers() {
		if def.GetId() == automationID {
			return def
		}
	}
	return nil
}

func (r Runner) promptMissingInputsAndConfirm(opts commandOptions, def *corev1.TriggerDefinition, inputs map[string]string, yes bool) error {
	reader := bufio.NewReader(r.stdin())
	interactive := !opts.NonInteractive && isTerminal(r.stdin())

	for _, spec := range def.GetInputs() {
		if _, ok := inputs[spec.GetName()]; ok {
			continue
		}
		if !spec.GetRequired() || spec.GetDefaultValue() != "" {
			continue
		}
		if !interactive {
			return userError("invalid_argument", fmt.Sprintf("required input %q is missing", spec.GetName()), fmt.Sprintf("Pass --input %s=<value>.", spec.GetName()))
		}
		prompt := spec.GetName()
		if len(spec.GetOptions()) > 0 {
			prompt += " (" + strings.Join(spec.GetOptions(), "|") + ")"
		}
		if spec.GetDescription() != "" {
			prompt += " - " + spec.GetDescription()
		}
		fmt.Fprintf(r.stderr(), "%s: ", prompt)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		val := strings.TrimSpace(line)
		if val == "" {
			return userError("invalid_argument", fmt.Sprintf("required input %q is empty", spec.GetName()), "")
		}
		inputs[spec.GetName()] = val
	}

	if confirm := def.GetManual().GetConfirm(); confirm != "" && !yes {
		if !interactive {
			return userError("confirmation_required", confirm, "Pass --yes to confirm non-interactively.")
		}
		fmt.Fprintf(r.stderr(), "%s [y/N]: ", confirm)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		if ans != "y" && ans != "yes" {
			return userError("canceled", "automation run canceled", "")
		}
	}
	return nil
}

func (r Runner) runTriggerRunAction(ctx context.Context, opts commandOptions, runID, action string) error {
	var run *corev1.TriggerRun
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		switch action {
		case "rerun":
			run, err = s.Trigger().RerunTriggerRun(s.Ctx(), &corev1.RerunTriggerRunRequest{RunId: strings.TrimSpace(runID)})
		default:
			run, err = s.Trigger().CancelTriggerRun(s.Ctx(), &corev1.CancelTriggerRunRequest{RunId: strings.TrimSpace(runID)})
		}
		return triggerError(err)
	}); err != nil {
		return err
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

// fullTriggerChangesetID turns a changeset handle into its id.
func (s *Session) fullTriggerChangesetID(handle string) (string, error) {
	cs, err := s.GetChangeset(strings.TrimSpace(handle))
	if err != nil {
		return "", triggerError(err)
	}
	return cs.Id, nil
}

var triggerErrorHints = domainErrorOverrides{
	codes.Unimplemented: {
		Message: "this server does not run automations",
		Hint:    "Upgrade the server.",
	},
}

func triggerError(err error) error {
	return withDomainErrorHints(err, triggerErrorHints)
}
