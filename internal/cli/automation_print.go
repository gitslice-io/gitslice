package cli

import (
	"fmt"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

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

func (r Runner) printRunLogChunk(l *corev1.AutomationRunLog) {
	if l.Stream == "stderr" {
		fmt.Fprint(r.stderr(), l.Chunk)
	} else {
		fmt.Fprint(r.Stdout, l.Chunk)
	}
}

func (r Runner) printAutomationRun(opts commandOptions, verb string, run *corev1.AutomationRun) error {
	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, automationRunToOutput(run))
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "%s run %s of %s\n", verb, run.Id, run.AutomationId)
	}
	return nil
}

func (r Runner) printAutomationRunDetails(out automationRunOutput) {
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
}

func (r Runner) printAutomationTemplateInspect(resp *corev1.InspectAutomationTemplateResponse, tpl automationOutput) {
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
}
