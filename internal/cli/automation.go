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

// automationCommand shows folder automations and their runs, and runs manual
// automations by hand (design/34_triggers.md).
func (r Runner) automationCommand(opts *commandOptions) *cobra.Command {
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
			return r.runAutomationList(cmd.Context(), *opts, folder, lsRef)
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
			return r.runAutomationRuns(cmd.Context(), *opts, runsAccount, runsAuto, runsChangeset, runsLimit)
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
			return r.runAutomationShow(cmd.Context(), *opts, args[0])
		},
	}

	var logsFollow bool
	logsCmd := &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Print an automation run's log",
		Args:  exactArgs(1, "gs automation logs <run-id> [--follow]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAutomationLogs(cmd.Context(), *opts, args[0], logsFollow)
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
			return r.runAutomationRun(cmd.Context(), *opts, runAccount, args[0], runRef, runChangeset, runFromChangeset, runInputs, runYes, runFollow)
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
			return r.runAutomationRunAction(cmd.Context(), *opts, args[0], "rerun")
		},
	}
	cancelCmd := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel an automation run that has not finished",
		Args:  exactArgs(1, "gs automation cancel <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAutomationRunAction(cmd.Context(), *opts, args[0], "cancel")
		},
	}

	var inspectAccount string
	inspectCmd := &cobra.Command{
		Use:   "inspect <uses-ref>",
		Short: "Inspect a reusable (on: [call]) automation template",
		Long: "Resolve a reusable automation template reference (/<account>[/<dir>...]/<name>[@<tag>|@sha256:...])\n" +
			"and show its inputs, secret slots, network requirements, resolved commit, and available immutable tags.",
		Args: exactArgs(1, "gs automation inspect <uses-ref> [--account <caller-account>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAutomationInspect(cmd.Context(), *opts, args[0], inspectAccount)
		},
	}
	inspectCmd.Flags().StringVar(&inspectAccount, "account", "", "caller account for unpinned same-account lookup")

	autoCmd.AddCommand(lsCmd, runsCmd, showCmd, logsCmd, runCmd, rerunCmd, cancelCmd, inspectCmd)
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

type automationUsesOutput struct {
	Ref  string            `json:"ref"`
	With map[string]string `json:"with,omitempty"`
}

type automationStepOutput struct {
	ID        string                `json:"id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Condition string                `json:"if,omitempty"`
	Action    string                `json:"action"`
	Uses      *automationUsesOutput `json:"uses,omitempty"`
	Bash      *automationBashOutput `json:"bash,omitempty"`
	Worker    string                `json:"worker,omitempty"`
	URL       string                `json:"url,omitempty"`
	Checks    []string              `json:"checks,omitempty"`
}

type automationOutput struct {
	ID             string                  `json:"id"`
	Folder         string                  `json:"folder"`
	Description    string                  `json:"description,omitempty"`
	On             []string                `json:"on"`
	Paths          []string                `json:"paths,omitempty"`
	Changes        []string                `json:"changes,omitempty"`
	Action         string                  `json:"action"`
	Worker         string                  `json:"worker,omitempty"`
	URL            string                  `json:"url,omitempty"`
	Bash           *automationBashOutput   `json:"bash,omitempty"`
	Uses           *automationUsesOutput   `json:"uses,omitempty"`
	Steps          []automationStepOutput  `json:"steps,omitempty"`
	Egress         string                  `json:"egress,omitempty"`
	Checks         []string                `json:"checks,omitempty"`
	Secrets        []string                `json:"secrets,omitempty"`
	SecretBindings map[string]string       `json:"secret_bindings,omitempty"`
	Reveal         []string                `json:"reveal,omitempty"`
	Network        []string                `json:"network,omitempty"`
	Inputs         []automationInputOutput `json:"inputs,omitempty"`
	Manual         *automationManualOutput `json:"manual,omitempty"`
	Concurrency    string                  `json:"concurrency"`
}

func automationDefToOutput(t *corev1.AutomationDefinition) automationOutput {
	out := automationOutput{
		ID: t.GetId(), Folder: t.GetFolder(), Description: t.GetDescription(),
		On: append([]string(nil), t.GetOn()...), Paths: append([]string(nil), t.GetPaths()...),
		Changes: append([]string(nil), t.GetChanges()...), Action: t.GetAction(),
		Worker: t.GetWorker(), URL: t.GetHttpUrl(), Egress: t.GetEgress(),
		Checks: append([]string(nil), t.GetChecks()...), Secrets: append([]string(nil), t.GetSecrets()...),
		SecretBindings: t.GetSecretBindings(),
		Reveal:         append([]string(nil), t.GetReveal()...), Network: append([]string(nil), t.GetNetwork()...),
		Concurrency: t.GetConcurrency(),
	}
	if b := t.GetBash(); b != nil {
		out.Bash = &automationBashOutput{
			Runtime: b.GetRuntime(), Size: b.GetSize(), Checkout: b.GetCheckout(),
			Script: b.GetScript(), WorkingDir: b.GetWorkingDir(), Include: append([]string(nil), b.GetInclude()...),
		}
	}
	if u := t.GetUses(); u != nil {
		out.Uses = &automationUsesOutput{Ref: u.GetRef(), With: u.GetWith()}
	}
	for _, st := range t.GetSteps() {
		so := automationStepOutput{
			ID: st.GetId(), Name: st.GetName(), Condition: st.GetCondition(),
			Action: st.GetAction(), Worker: st.GetWorker(), URL: st.GetHttpUrl(),
			Checks: append([]string(nil), st.GetChecks()...),
		}
		if u := st.GetUses(); u != nil {
			so.Uses = &automationUsesOutput{Ref: u.GetRef(), With: u.GetWith()}
		}
		if b := st.GetBash(); b != nil {
			so.Bash = &automationBashOutput{
				Runtime: b.GetRuntime(), Size: b.GetSize(), Checkout: b.GetCheckout(),
				Script: b.GetScript(), WorkingDir: b.GetWorkingDir(), Include: append([]string(nil), b.GetInclude()...),
			}
		}
		out.Steps = append(out.Steps, so)
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

func automationActionSummary(t automationOutput) string {
	switch t.Action {
	case "http":
		return t.URL
	case "check":
		return strings.Join(t.Checks, ",")
	case "uses":
		if t.Uses != nil {
			return t.Uses.Ref
		}
		return ""
	case "steps":
		if len(t.Steps) == 1 {
			return "1 step"
		}
		return fmt.Sprintf("%d steps", len(t.Steps))
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

type automationRunOutput struct {
	ID               string                `json:"id"`
	Automation       string                `json:"automation"`
	Status           string                `json:"status"`
	Event            string                `json:"event"`
	EventID          string                `json:"event_id"`
	Action           string                `json:"action"`
	TriggeredBy      string                `json:"triggered_by,omitempty"`
	TriggeredByKind  string                `json:"triggered_by_kind,omitempty"`
	Inputs           map[string]string     `json:"inputs,omitempty"`
	Outputs          map[string]string     `json:"outputs,omitempty"`
	Changeset        string                `json:"changeset,omitempty"`
	Attempt          int32                 `json:"attempt"`
	MaxAttempts      int32                 `json:"max_attempts"`
	Error            string                `json:"error,omitempty"`
	CreatedAt        string                `json:"created_at"`
	FinishedAt       string                `json:"finished_at,omitempty"`
	Definition       string                `json:"definition_commit,omitempty"`
	DetailsURL       string                `json:"details_url,omitempty"`
	TemplateID       string                `json:"template_id,omitempty"`
	TemplateCommitID string                `json:"template_commit_id,omitempty"`
	ParentRunID      string                `json:"parent_run_id,omitempty"`
	StepIndex        int32                 `json:"step_index,omitempty"`
	StepID           string                `json:"step_id,omitempty"`
	Steps            []automationRunOutput `json:"steps,omitempty"`
}

func automationRunToOutput(run *corev1.AutomationRun) automationRunOutput {
	return automationRunOutput{
		ID: run.Id, Automation: run.AutomationId, Status: run.Status, Event: run.EventKind, EventID: run.EventId, Action: run.Action,
		TriggeredBy: run.TriggeredBy, TriggeredByKind: run.TriggeredByKind, Inputs: run.Inputs, Outputs: run.Outputs,
		Changeset: model.ShortChangesetID(run.ChangesetId), Attempt: run.Attempt, MaxAttempts: run.MaxAttempts,
		Error: run.Error, CreatedAt: run.CreatedAt, FinishedAt: run.FinishedAt, Definition: run.DefinitionCommitId,
		DetailsURL: run.DetailsUrl, TemplateID: run.TemplateId, TemplateCommitID: run.TemplateCommitId,
		ParentRunID: run.ParentRunId, StepIndex: run.StepIndex, StepID: run.StepId,
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

func (r Runner) runAutomationList(ctx context.Context, opts commandOptions, rawFolder, ref string) error {
	folder, err := r.automationFolder(rawFolder)
	if err != nil {
		return err
	}
	var resp *corev1.ListAutomationsResponse
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		resp, err = s.Automation().ListAutomations(s.Ctx(), &corev1.ListAutomationsRequest{Path: folder, Ref: ref})
		return automationError(err)
	}); err != nil {
		return err
	}
	out := make([]automationOutput, 0, len(resp.Automations))
	for _, t := range resp.Automations {
		out = append(out, automationDefToOutput(t))
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

func (r Runner) runAutomationRuns(ctx context.Context, opts commandOptions, account, automation, changeset string, limit int) error {
	var resp *corev1.ListAutomationRunsResponse
	if err := r.withSession(ctx, func(s *Session) error {
		req := &corev1.ListAutomationRunsRequest{AutomationId: strings.TrimSpace(automation), Limit: int32(limit)}
		if changeset != "" {
			id, err := s.fullAutomationChangesetID(changeset)
			if err != nil {
				return err
			}
			req.ChangesetId = id
		} else {
			var err error
			if req.Account, err = r.resolveAutomationAccount(s, account); err != nil {
				return err
			}
			if req.AutomationId != "" && !strings.Contains(req.AutomationId, "/") {
				resolvedID, _ := r.resolveAutomationIDAndDef(s, req.Account, req.AutomationId, "", false)
				req.AutomationId = resolvedID
			}
		}
		var err error
		resp, err = s.Automation().ListAutomationRuns(s.Ctx(), req)
		return automationError(err)
	}); err != nil {
		return err
	}
	out := make([]automationRunOutput, 0, len(resp.Runs))
	for _, run := range resp.Runs {
		out = append(out, automationRunToOutput(run))
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

func formatKVMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

func (r Runner) runAutomationShow(ctx context.Context, opts commandOptions, runID string) error {
	var run *corev1.AutomationRun
	var stepRuns []*corev1.AutomationRun
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		run, err = s.Automation().GetAutomationRun(s.Ctx(), &corev1.GetAutomationRunRequest{RunId: strings.TrimSpace(runID)})
		if err != nil {
			return automationError(err)
		}
		if run.Action == "steps" {
			resp, serr := s.Automation().ListAutomationRuns(s.Ctx(), &corev1.ListAutomationRunsRequest{
				ParentRunId: run.Id,
				Limit:       200,
			})
			if serr == nil {
				stepRuns = resp.Runs
			}
		}
		return nil
	}); err != nil {
		return err
	}
	out := automationRunToOutput(run)
	for _, sr := range stepRuns {
		out.Steps = append(out.Steps, automationRunToOutput(sr))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "run: %s\nautomation: %s (%s)\nstatus: %s\nevent: %s %s\nattempt: %d of %d\ncreated: %s\n",
		out.ID, out.Automation, out.Action, out.Status, out.Event, out.EventID, out.Attempt, out.MaxAttempts, out.CreatedAt)
	if out.TemplateID != "" {
		tpl := out.TemplateID
		if out.TemplateCommitID != "" {
			tpl += " @" + out.TemplateCommitID
		}
		fmt.Fprintf(r.Stdout, "template: %s\n", tpl)
	}
	if out.ParentRunID != "" {
		stepLabel := fmt.Sprintf("#%d", out.StepIndex)
		if out.StepID != "" {
			stepLabel += " " + out.StepID
		}
		fmt.Fprintf(r.Stdout, "parent_run: %s (step %s)\n", out.ParentRunID, stepLabel)
	}
	if out.TriggeredBy != "" {
		actor := out.TriggeredBy
		if out.TriggeredByKind != "" {
			actor += " (" + out.TriggeredByKind + ")"
		}
		fmt.Fprintf(r.Stdout, "triggered_by: %s\n", actor)
	}
	if len(out.Inputs) > 0 {
		fmt.Fprintf(r.Stdout, "inputs: %s\n", formatKVMap(out.Inputs))
	}
	if len(out.Outputs) > 0 {
		fmt.Fprintf(r.Stdout, "outputs: %s\n", formatKVMap(out.Outputs))
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
	if len(out.Steps) > 0 {
		fmt.Fprintln(r.Stdout, "steps:")
		for i, st := range out.Steps {
			num := int(st.StepIndex)
			if num <= 0 {
				num = i + 1
			}
			name := st.StepID
			if name == "" {
				name = fmt.Sprintf("step_%d", num)
			}
			line := fmt.Sprintf("  %d. %s\t%s\t%s\t%s", num, name, st.Status, st.Action, st.ID)
			if len(st.Outputs) > 0 {
				line += "\toutputs " + formatKVMap(st.Outputs)
			}
			if st.Error != "" {
				line += "\t" + oneLineSummary(st.Error)
			}
			fmt.Fprintln(r.Stdout, line)
		}
	}
	return nil
}

func (r Runner) runAutomationInspect(ctx context.Context, opts commandOptions, rawRef, rawAccount string) error {
	ref := strings.TrimSpace(rawRef)
	var resp *corev1.InspectAutomationTemplateResponse
	if err := r.withSession(ctx, func(s *Session) error {
		callerAccount := strings.TrimSpace(rawAccount)
		if callerAccount != "" {
			var err error
			if callerAccount, err = r.resolveAutomationAccount(s, callerAccount); err != nil {
				return err
			}
		} else if acc, err := r.resolveAutomationAccount(s, ""); err == nil {
			callerAccount = acc
		}
		var err error
		resp, err = s.Automation().InspectAutomationTemplate(s.Ctx(), &corev1.InspectAutomationTemplateRequest{
			Ref:           ref,
			CallerAccount: callerAccount,
		})
		return automationError(err)
	}); err != nil {
		return err
	}
	tpl := automationDefToOutput(resp.GetTemplate())
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"template":       tpl,
			"account":        resp.GetAccount(),
			"folder":         resp.GetFolder(),
			"commit_id":      resp.GetCommitId(),
			"resolved_ref":   resp.GetResolvedRef(),
			"available_tags": resp.GetAvailableTags(),
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "template: %s\nresolved_ref: %s\ncommit: %s\nfolder: %s\naction: %s",
		tpl.ID, resp.GetResolvedRef(), resp.GetCommitId(), resp.GetFolder(), tpl.Action)
	if summary := automationActionSummary(tpl); summary != "" {
		fmt.Fprintf(r.Stdout, " (%s)", summary)
	}
	fmt.Fprintln(r.Stdout)
	if tpl.Description != "" {
		fmt.Fprintf(r.Stdout, "description: %s\n", tpl.Description)
	}
	if tpl.Egress != "" {
		fmt.Fprintf(r.Stdout, "egress: %s\n", tpl.Egress)
	}
	if len(tpl.Secrets) > 0 {
		fmt.Fprintf(r.Stdout, "secrets: %s\n", strings.Join(tpl.Secrets, ", "))
	}
	if len(tpl.Reveal) > 0 {
		fmt.Fprintf(r.Stdout, "reveal: %s\n", strings.Join(tpl.Reveal, ", "))
	}
	if len(tpl.Network) > 0 {
		fmt.Fprintf(r.Stdout, "network: %s\n", strings.Join(tpl.Network, ", "))
	}
	if len(tpl.Inputs) > 0 {
		fmt.Fprintln(r.Stdout, "inputs:")
		for _, in := range tpl.Inputs {
			req := "optional"
			if in.Required {
				req = "required"
			}
			line := fmt.Sprintf("  %s (%s, %s", in.Name, in.Type, req)
			if in.Default != "" {
				line += ", default=" + in.Default
			}
			if len(in.Options) > 0 {
				line += ", options=" + strings.Join(in.Options, "|")
			}
			line += ")"
			if in.Description != "" {
				line += " — " + in.Description
			}
			fmt.Fprintln(r.Stdout, line)
		}
	}
	if len(resp.GetAvailableTags()) > 0 {
		fmt.Fprintf(r.Stdout, "tags: %s\n", strings.Join(resp.GetAvailableTags(), ", "))
	}
	return nil
}

func (r Runner) runAutomationLogs(ctx context.Context, opts commandOptions, runID string, follow bool) error {
	if follow && !opts.jsonOutput() {
		_, err := r.followAutomationRun(ctx, runID)
		return err
	}
	var all []*corev1.AutomationRunLog
	if err := r.withSession(ctx, func(s *Session) error {
		var after int64
		for {
			resp, err := s.Automation().ListAutomationRunLogs(s.Ctx(), &corev1.ListAutomationRunLogsRequest{RunId: strings.TrimSpace(runID), AfterSeq: after})
			if err != nil {
				return automationError(err)
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

func (r Runner) printRunLogChunk(l *corev1.AutomationRunLog) {
	if l.Stream == "stderr" {
		fmt.Fprint(r.stderr(), l.Chunk)
	} else {
		fmt.Fprint(r.Stdout, l.Chunk)
	}
}

func (r Runner) followAutomationRun(ctx context.Context, runID string) (*corev1.AutomationRun, error) {
	runID = strings.TrimSpace(runID)
	var after int64
	var finalRun *corev1.AutomationRun
	shownDetails := ""
	childAfter := map[string]int64{}
	childHeader := map[string]bool{}
	childDetails := map[string]string{}
	childDone := map[string]bool{}
	drainChildren := func(s *Session) {
		resp, err := s.Automation().ListAutomationRuns(s.Ctx(), &corev1.ListAutomationRunsRequest{
			ParentRunId: runID,
			Limit:       200,
		})
		if err != nil {
			return
		}
		for i, child := range resp.Runs {
			num := int(child.StepIndex)
			if num <= 0 {
				num = i + 1
			}
			name := child.StepId
			if name == "" {
				name = fmt.Sprintf("step_%d", num)
			}
			if !childHeader[child.Id] {
				childHeader[child.Id] = true
				fmt.Fprintf(r.stderr(), "==> step %d: %s (%s)\n", num, name, child.Id)
			}
			for {
				lresp, lerr := s.Automation().ListAutomationRunLogs(s.Ctx(), &corev1.ListAutomationRunLogsRequest{
					RunId:    child.Id,
					AfterSeq: childAfter[child.Id],
				})
				if lerr != nil || len(lresp.Logs) == 0 {
					break
				}
				for _, l := range lresp.Logs {
					r.printRunLogChunk(l)
					childAfter[child.Id] = l.Seq
				}
			}
			if child.DetailsUrl != "" && child.DetailsUrl != childDetails[child.Id] {
				childDetails[child.Id] = child.DetailsUrl
				fmt.Fprintf(r.stderr(), "details (%s): %s\n", name, child.DetailsUrl)
			}
			if automationRunFinal(child.Status) && !childDone[child.Id] {
				childDone[child.Id] = true
				msg := fmt.Sprintf("<== step %d: %s %s", num, name, child.Status)
				if child.Error != "" {
					msg += " (" + oneLineSummary(child.Error) + ")"
				}
				fmt.Fprintln(r.stderr(), msg)
			}
		}
	}
	err := r.withSession(ctx, func(s *Session) error {
		for {
			resp, err := s.Automation().ListAutomationRunLogs(s.Ctx(), &corev1.ListAutomationRunLogsRequest{RunId: runID, AfterSeq: after})
			if err != nil {
				return automationError(err)
			}
			for _, l := range resp.Logs {
				r.printRunLogChunk(l)
				after = l.Seq
			}
			run, err := s.Automation().GetAutomationRun(s.Ctx(), &corev1.GetAutomationRunRequest{RunId: runID})
			if err != nil {
				return automationError(err)
			}
			finalRun = run
			if run.DetailsUrl != "" && run.DetailsUrl != shownDetails {
				shownDetails = run.DetailsUrl
				fmt.Fprintf(r.stderr(), "details: %s\n", run.DetailsUrl)
			}
			if run.Action == "steps" {
				drainChildren(s)
			}
			if automationRunFinal(run.Status) {
				// Drain any final lines emitted right before status flipped.
				tail, terr := s.Automation().ListAutomationRunLogs(s.Ctx(), &corev1.ListAutomationRunLogsRequest{RunId: runID, AfterSeq: after})
				if terr == nil {
					for _, l := range tail.Logs {
						r.printRunLogChunk(l)
						after = l.Seq
					}
				}
				if run.Action == "steps" {
					drainChildren(s)
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

func (r Runner) runAutomationRun(ctx context.Context, opts commandOptions, account, triggerID, ref, changeset, fromChangeset string, rawInputs []string, yes, follow bool) error {
	inputs := map[string]string{}
	for _, raw := range rawInputs {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return userError("invalid_argument", "--input "+raw+" is not key=value", "")
		}
		inputs[strings.TrimSpace(key)] = value
	}
	triggerID = strings.TrimSpace(triggerID)
	var run *corev1.AutomationRun
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
		req := &corev1.RunAutomationRequest{Account: account, AutomationId: triggerID, Ref: ref, Inputs: inputs, DefinitionFromChangeset: fromChangeset != ""}
		if changeset != "" {
			if req.ChangesetId, err = s.fullAutomationChangesetID(changeset); err != nil {
				return err
			}
		}
		run, err = s.Automation().RunAutomation(s.Ctx(), req)
		return automationError(err)
	}); err != nil {
		return err
	}
	if follow && !opts.jsonOutput() {
		if !opts.Quiet {
			fmt.Fprintf(r.stderr(), "started run %s of %s\n", run.Id, run.AutomationId)
		}
		_, err := r.followAutomationRun(ctx, run.Id)
		return err
	}
	return r.printAutomationRun(opts, "queued", run)
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

func (r Runner) resolveAutomationIDAndDef(s *Session, account, triggerID, ref string, fromChangeset bool) (string, *corev1.AutomationDefinition) {
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
		resp, err := s.Automation().ListAutomations(s.Ctx(), &corev1.ListAutomationsRequest{Path: folder, Ref: ref})
		if err == nil {
			var best *corev1.AutomationDefinition
			for _, def := range resp.GetAutomations() {
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

func lookupAutomationDef(s *Session, account, automationID, ref string) *corev1.AutomationDefinition {
	folder := automationFolderOf(account, automationID)
	resp, err := s.Automation().ListAutomations(s.Ctx(), &corev1.ListAutomationsRequest{Path: folder, Ref: ref})
	if err != nil {
		return nil
	}
	for _, def := range resp.GetAutomations() {
		if def.GetId() == automationID {
			return def
		}
	}
	return nil
}

func (r Runner) promptMissingInputsAndConfirm(opts commandOptions, def *corev1.AutomationDefinition, inputs map[string]string, yes bool) error {
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

func (r Runner) runAutomationRunAction(ctx context.Context, opts commandOptions, runID, action string) error {
	var run *corev1.AutomationRun
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		switch action {
		case "rerun":
			run, err = s.Automation().RerunAutomationRun(s.Ctx(), &corev1.RerunAutomationRunRequest{RunId: strings.TrimSpace(runID)})
		default:
			run, err = s.Automation().CancelAutomationRun(s.Ctx(), &corev1.CancelAutomationRunRequest{RunId: strings.TrimSpace(runID)})
		}
		return automationError(err)
	}); err != nil {
		return err
	}
	verb := "queued"
	if action == "cancel" {
		verb = "canceled"
	}
	return r.printAutomationRun(opts, verb, run)
}

func (r Runner) printAutomationRun(opts commandOptions, verb string, run *corev1.AutomationRun) error {
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, automationRunToOutput(run))
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "%s run %s of %s\n", verb, run.Id, run.AutomationId)
	}
	return nil
}

// fullAutomationChangesetID turns a changeset handle into its id.
func (s *Session) fullAutomationChangesetID(handle string) (string, error) {
	cs, err := s.GetChangeset(strings.TrimSpace(handle))
	if err != nil {
		return "", automationError(err)
	}
	return cs.Id, nil
}

var triggerErrorHints = domainErrorOverrides{
	codes.Unimplemented: {
		Message: "this server does not run automations",
		Hint:    "Upgrade the server.",
	},
}

func automationError(err error) error {
	return withDomainErrorHints(err, triggerErrorHints)
}
