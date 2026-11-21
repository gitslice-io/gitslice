package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gitslice.io/gitslice/internal/outbound"
	"gitslice.io/gitslice/internal/storage"
)

// runPipeline claims a queued ActionSteps pipeline run, transitions it to
// waiting, and starts its first eligible step.
func (e *Engine) runPipeline(ctx context.Context, queued storage.AutomationRun) {
	var spec RunSpec
	if err := json.Unmarshal(queued.Spec, &spec); err != nil {
		_, _ = e.stores.Automations.UpdateAutomationRun(ctx, queued.ID, []string{storage.AutomationRunQueued}, storage.AutomationRunUpdate{
			Status: storage.AutomationRunErrored, Error: "the pipeline run has an unreadable spec",
		})
		return
	}
	if spec.Automation.Concurrency == ConcurrencySerial {
		var open []storage.AutomationRun
		var err error
		if queued.ConcurrencyGroup != "" {
			open, err = e.stores.Automations.OpenAutomationRunsInGroup(ctx, queued.Account, queued.ConcurrencyGroup)
		} else {
			open, err = e.stores.Automations.OpenAutomationRuns(ctx, queued.Account, queued.AutomationID, "")
		}
		if err != nil || mustWait(queued, open) {
			return
		}
	}
	if active, err := e.stores.Automations.CountActiveAutomationRuns(ctx, queued.Account); err != nil || active >= MaxActivePerAccount {
		return
	}
	parent, err := e.stores.Automations.ClaimAutomationRun(ctx, queued.ID, RunLease)
	if err != nil {
		return
	}
	timeout := spec.Automation.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	updated, err := e.stores.Automations.UpdateAutomationRun(ctx, parent.ID, []string{storage.AutomationRunRunning}, storage.AutomationRunUpdate{
		Status:   storage.AutomationRunWaiting,
		Deadline: time.Now().Add(timeout),
	})
	if err != nil {
		return
	}
	e.launchNextPipelineStep(ctx, *updated, spec, 0, false, nil)
}

// OnRunFinished notifies a parent pipeline run whenever one of its child step
// runs reaches a terminal status, advancing to the next eligible step or
// finalizing the pipeline. Safe to call on non-child runs (no-op) and safe to
// call multiple times (idempotent).
func (e *Engine) OnRunFinished(ctx context.Context, finished storage.AutomationRun) {
	if finished.ParentRunID == "" || !storage.AutomationRunFinal(finished.Status) {
		return
	}
	parent, err := e.stores.Automations.GetAutomationRun(ctx, finished.ParentRunID)
	if err != nil || storage.AutomationRunFinal(parent.Status) {
		return
	}
	var spec RunSpec
	if err := json.Unmarshal(parent.Spec, &spec); err != nil {
		_, _ = e.stores.Automations.UpdateAutomationRun(ctx, parent.ID, storage.AutomationRunOpen, storage.AutomationRunUpdate{
			Status: storage.AutomationRunErrored, Error: "pipeline spec is unreadable",
		})
		return
	}
	children, err := e.stores.Automations.ListAutomationRuns(ctx, storage.AutomationRunFilter{
		ParentRunID: parent.ID,
		Limit:       MaxSteps + 5,
	})
	if err != nil {
		slog.WarnContext(ctx, "list pipeline child runs", "parent", parent.ID, "error", err)
		return
	}
	anyFailed := false
	maxStepIndex := 0
	stepOutputs := map[string]map[string]string{}
	for _, child := range children {
		if child.StepIndex > maxStepIndex {
			maxStepIndex = child.StepIndex
		}
		if !storage.AutomationRunFinal(child.Status) {
			// Another step is still open; wait for it to finish.
			return
		}
		if child.Status != storage.AutomationRunSucceeded {
			anyFailed = true
		}
		if child.StepID != "" && len(child.Outputs) > 0 {
			stepOutputs[child.StepID] = cloneStringMap(child.Outputs)
		}
	}
	label := finished.StepID
	if label == "" {
		label = fmt.Sprintf("step-%d", finished.StepIndex)
	}
	line := fmt.Sprintf("=== Step %d/%d (%s): %s ===\n", finished.StepIndex, len(spec.Automation.Action.Steps), label, finished.Status)
	if finished.Error != "" {
		line = fmt.Sprintf("=== Step %d/%d (%s): %s (%s) ===\n", finished.StepIndex, len(spec.Automation.Action.Steps), label, finished.Status, finished.Error)
	}
	_, _ = e.stores.Automations.AppendAutomationRunLog(ctx, parent.ID, "stdout", line)

	e.launchNextPipelineStep(ctx, *parent, spec, maxStepIndex, anyFailed, stepOutputs)
}

func (e *Engine) launchNextPipelineStep(ctx context.Context, parent storage.AutomationRun, spec RunSpec, nextZeroBased int, anyFailed bool, stepOutputs map[string]map[string]string) {
	steps := spec.Automation.Action.Steps
	total := len(steps)
	lastErr := ""

	for i := nextZeroBased; i < total; i++ {
		step := steps[i]
		cond := step.If
		if cond == "" {
			cond = StepIfSuccess
		}
		shouldRun := cond == StepIfAlways || (cond == StepIfSuccess && !anyFailed) || (cond == StepIfFailure && anyFailed)
		if !shouldRun {
			_, _ = e.stores.Automations.AppendAutomationRunLog(ctx, parent.ID, "stdout",
				fmt.Sprintf("=== Step %d/%d (%s): skipped (if: %s) ===\n", i+1, total, step.ID, cond))
			continue
		}

		child, err := e.buildPipelineStepRun(ctx, parent, spec, i, step, stepOutputs)
		if err != nil {
			anyFailed = true
			lastErr = fmt.Sprintf("step %s: %v", step.ID, err)
			_, _ = e.stores.Automations.AppendAutomationRunLog(ctx, parent.ID, "stderr",
				fmt.Sprintf("=== Step %d/%d (%s): errored (%v) ===\n", i+1, total, step.ID, err))
			continue
		}

		created, err := e.stores.Automations.CreateAutomationRuns(ctx, []storage.AutomationRun{child})
		if err != nil {
			slog.WarnContext(ctx, "create pipeline step run", "parent", parent.ID, "step", step.ID, "error", err)
			_, _ = e.stores.Automations.UpdateAutomationRun(ctx, parent.ID, storage.AutomationRunOpen, storage.AutomationRunUpdate{
				Status: storage.AutomationRunErrored, Error: fmt.Sprintf("step %s: %v", step.ID, err),
			})
			return
		}
		if len(created) == 0 {
			// Another goroutine already created this step (idempotent).
			return
		}
		started := created[0]
		stepLabel := step.ID
		if step.Name != "" {
			stepLabel = fmt.Sprintf("%s (%s)", step.Name, step.ID)
		}
		_, _ = e.stores.Automations.AppendAutomationRunLog(ctx, parent.ID, "stdout",
			fmt.Sprintf("=== Step %d/%d: %s [%s] ===\n", i+1, total, stepLabel, started.ID))

		e.dispatchRun(ctx, started)
		return
	}

	// All steps finished or skipped.
	finalStatus := storage.AutomationRunSucceeded
	finalErr := ""
	if anyFailed {
		finalStatus = storage.AutomationRunFailed
		if lastErr != "" {
			finalErr = lastErr
		} else {
			finalErr = "one or more pipeline steps failed"
		}
	}
	_, _ = e.stores.Automations.UpdateAutomationRun(ctx, parent.ID, storage.AutomationRunOpen, storage.AutomationRunUpdate{
		Status: finalStatus,
		Error:  finalErr,
	})
}

func (e *Engine) buildPipelineStepRun(ctx context.Context, parent storage.AutomationRun, parentSpec RunSpec, index int, step StepSpec, stepOutputs map[string]map[string]string) (storage.AutomationRun, error) {
	stepAuto := Automation{
		Description:     step.Name,
		On:              parentSpec.Automation.On,
		Secrets:         append([]string(nil), step.Secrets...),
		SecretBindings:  cloneStringMap(step.SecretBindings),
		Reveal:          append([]string(nil), step.Reveal...),
		Network:         append([]string(nil), step.Network...),
		Egress:          step.Egress,
		Concurrency:     ConcurrencyParallel,
		Timeout:         step.Timeout,
		TimeoutExplicit: step.TimeoutExplicit,
		Action:          step.Action,
	}
	if !stepAuto.TimeoutExplicit {
		if stepAuto.Action.Kind == ActionBash {
			stepAuto.Timeout = DefaultBashTimeout
		} else {
			stepAuto.Timeout = DefaultTimeout
		}
	}

	stepInputs := cloneStringMap(parentSpec.Inputs)
	templateID, templateFolder, templateCommitID, templateTreeID := "", "", "", ""

	switch stepAuto.Action.Kind {
	case ActionUses:
		ref, err := ParseUsesRef(stepAuto.Action.Uses.Ref)
		if err != nil {
			return storage.AutomationRun{}, err
		}
		treeID := parentSpec.DefinitionTree
		if treeID == "" && parent.DefinitionCommitID != "" && e.stores.Repository != nil {
			treeID, err = e.stores.Repository.RootTreeForCommit(ctx, parent.DefinitionCommitID)
			if err != nil {
				return storage.AutomationRun{}, err
			}
		}
		loaded, err := ResolveTemplate(ctx, e.opts.Tree, e.templates, parent.Account, parent.DefinitionCommitID, treeID, ref)
		if err != nil {
			return storage.AutomationRun{}, err
		}
		bound, err := BindTemplate(stepAuto, parent.Account, loaded, parentSpec.Inputs, stepOutputs)
		if err != nil {
			return storage.AutomationRun{}, err
		}
		stepAuto = bound.Automation
		stepAuto.Concurrency = ConcurrencyParallel
		stepAuto.ConcurrencyGroup = ""
		stepInputs = bound.Inputs
		templateID = bound.TemplateID
		templateFolder = bound.TemplateFolder
		templateCommitID = bound.TemplateCommitID
		templateTreeID = bound.TemplateTreeID

	case ActionBash:
		if stepAuto.Action.Bash != nil {
			bCopy := *stepAuto.Action.Bash
			bCopy.Run = InterpolateExpressions(bCopy.Run, parentSpec.Inputs, stepOutputs)
			if len(bCopy.Env) > 0 {
				envCopy := make(map[string]string, len(bCopy.Env))
				for k, v := range bCopy.Env {
					envCopy[k] = InterpolateExpressions(v, parentSpec.Inputs, stepOutputs)
				}
				bCopy.Env = envCopy
			}
			stepAuto.Action.Bash = &bCopy
		}

	case ActionHTTP:
		if stepAuto.Action.HTTP != nil {
			hCopy := *stepAuto.Action.HTTP
			hCopy.URL = InterpolateExpressions(hCopy.URL, parentSpec.Inputs, stepOutputs)
			if len(hCopy.Headers) > 0 {
				hdrs := make(map[string]string, len(hCopy.Headers))
				for k, v := range hCopy.Headers {
					hdrs[k] = InterpolateExpressions(v, parentSpec.Inputs, stepOutputs)
				}
				hCopy.Headers = hdrs
			}
			stepAuto.Action.HTTP = &hCopy
		}
	}

	// Enrich payload with step outputs and resolved step inputs.
	payloadMap := map[string]any{}
	if len(parent.Payload) > 0 {
		_ = json.Unmarshal(parent.Payload, &payloadMap)
	}
	if stepInputs != nil {
		payloadMap["inputs"] = stepInputs
	}
	if len(stepOutputs) > 0 {
		stepsPayload := map[string]any{}
		for sid, outs := range stepOutputs {
			stepsPayload[sid] = map[string]any{"outputs": outs}
		}
		payloadMap["steps"] = stepsPayload
	}
	payloadMap["step"] = map[string]any{
		"id":            step.ID,
		"name":          step.Name,
		"index":         index + 1,
		"parent_run_id": parent.ID,
	}
	body, err := json.Marshal(payloadMap)
	if err != nil {
		return storage.AutomationRun{}, err
	}

	childSpec, err := json.Marshal(RunSpec{
		Name:             step.ID,
		Automation:       stepAuto,
		Depth:            parentSpec.Depth,
		Inputs:           stepInputs,
		DefinitionTree:   parentSpec.DefinitionTree,
		TemplateID:       templateID,
		TemplateFolder:   templateFolder,
		TemplateCommitID: templateCommitID,
		TemplateTreeID:   templateTreeID,
	})
	if err != nil {
		return storage.AutomationRun{}, err
	}

	maxAttempts := 1 + stepAuto.Retry
	switch stepAuto.Action.Kind {
	case ActionHTTP:
		maxAttempts = len(outbound.DefaultRetryDelays) + 1
	case ActionCheck:
		maxAttempts = 1
	}

	return storage.AutomationRun{
		Account:            parent.Account,
		AutomationID:       parent.AutomationID,
		Folder:             parent.Folder,
		Action:             stepAuto.Action.Kind,
		EventID:            fmt.Sprintf("%s:step:%d", parent.ID, index+1),
		EventKind:          parent.EventKind,
		TriggeredBy:        parent.TriggeredBy,
		TriggeredByKind:    parent.TriggeredByKind,
		ChangesetID:        parent.ChangesetID,
		PatchsetID:         parent.PatchsetID,
		CommitID:           parent.CommitID,
		TargetRef:          parent.TargetRef,
		DefinitionCommitID: parent.DefinitionCommitID,
		TemplateID:         templateID,
		TemplateCommitID:   templateCommitID,
		ParentRunID:        parent.ID,
		StepIndex:          index + 1,
		StepID:             step.ID,
		Spec:               childSpec,
		Payload:            body,
		Status:             storage.AutomationRunQueued,
		MaxAttempts:        maxAttempts,
	}, nil
}

func (e *Engine) dispatchRun(ctx context.Context, run storage.AutomationRun) {
	switch run.Action {
	case ActionHTTP:
		e.sendHTTP(ctx, run)
	case ActionWorker, ActionBash:
		e.post(ctx, run)
	case ActionCheck:
		e.launch(ctx, run)
	case ActionSteps:
		e.runPipeline(ctx, run)
	}
}

func (e *Engine) cancelPipelineChildren(ctx context.Context, parentID, reason string) {
	children, err := e.stores.Automations.ListAutomationRuns(ctx, storage.AutomationRunFilter{
		ParentRunID: parentID,
		Limit:       MaxSteps + 5,
	})
	if err != nil {
		return
	}
	for _, child := range children {
		if !storage.AutomationRunFinal(child.Status) {
			if _, err := e.Cancel(ctx, child, reason); err != nil && !errors.Is(err, storage.ErrConflict) {
				slog.WarnContext(ctx, "cancel pipeline child run", "parent", parentID, "child", child.ID, "error", err)
			}
		}
	}
}
