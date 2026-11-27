package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/cli/clibase"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// triggerFolder reads a folder argument: canonical (/acme/x), or a folder of
// the workspace (default: the current directory).
func (r Runner) triggerFolder(raw string) (string, error) {
	return clibase.WorkspaceFolder(r.cwd(), raw)
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
	if opts.JSONOutput() {
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
	if opts.JSONOutput() {
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
	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	r.printAutomationRunDetails(out)
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
	if opts.JSONOutput() {
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
	r.printAutomationTemplateInspect(resp, tpl)
	return nil
}

func (r Runner) runAutomationLogs(ctx context.Context, opts commandOptions, runID string, follow bool) error {
	if follow && !opts.JSONOutput() {
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
	if opts.JSONOutput() {
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
	if follow && !opts.JSONOutput() {
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

// fullAutomationChangesetID turns a changeset handle into its id.
func (s *Session) fullAutomationChangesetID(handle string) (string, error) {
	cs, err := s.GetChangeset(strings.TrimSpace(handle))
	if err != nil {
		return "", automationError(err)
	}
	return cs.Id, nil
}
