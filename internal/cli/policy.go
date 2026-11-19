package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type folderOwnerRuleOutput struct {
	Paths        []string `json:"paths"`
	Owners       []string `json:"owners"`
	Require      int32    `json:"require"`
	RequireHuman int32    `json:"require_human,omitempty"`
}

type folderCheckOutput struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Folder     string   `json:"folder"`
	ConfigPath string   `json:"config_path"`
	Run        string   `json:"run"`
	Paths      []string `json:"paths,omitempty"`
	StartedBy  string   `json:"started_by,omitempty"`
	Required   bool     `json:"required"`
}

type folderSecretOutput struct {
	Name         string   `json:"name"`
	SecretID     string   `json:"secret_id,omitempty"`
	SourceFolder string   `json:"source_folder,omitempty"`
	Paths        []string `json:"paths,omitempty"`
}

type folderPolicyProblemOutput struct {
	Kind    string `json:"kind"`
	Folder  string `json:"folder"`
	File    string `json:"file"`
	Message string `json:"message"`
}

type folderPolicyOutput struct {
	Folder            string                      `json:"folder"`
	Account           string                      `json:"account"`
	CommitID          string                      `json:"commit_id"`
	OwnerConfigPath   string                      `json:"owner_config_path,omitempty"`
	OwnerSourceFolder string                      `json:"owner_source_folder,omitempty"`
	OwnerRules        []folderOwnerRuleOutput     `json:"owner_rules,omitempty"`
	Checks            []folderCheckOutput         `json:"checks,omitempty"`
	Triggers          []triggerOutput             `json:"triggers,omitempty"`
	Secrets           []folderSecretOutput        `json:"secrets,omitempty"`
	CanManageSecrets  bool                        `json:"can_manage_secrets,omitempty"`
	CIDaemonIDs       []string                    `json:"ci_daemon_ids,omitempty"`
	CIDaemonNames     []string                    `json:"ci_daemon_names,omitempty"`
	Problems          []folderPolicyProblemOutput `json:"problems,omitempty"`
}

// policyCommand shows everything bound to a folder at the head of a ref:
// path owners, checks (including required gates), triggers, effective secrets,
// and the account's default CI runner pool.
func (r Runner) policyCommand(opts *commandOptions) *cobra.Command {
	var ref string
	cmd := &cobra.Command{
		Use:   "policy [folder]",
		Short: "Show effective owners, checks, triggers, secrets, and CI runners for a folder (default: current directory)",
		Long: "Inspect the effective policy of a canonical folder (such as /acme/services/billing) or the current\n" +
			"directory inside a workspace: nearest .gitslice/owners.yaml rules, inherited .gitslice/checks.yaml\n" +
			"checks (including required gates), inherited .gitslice/triggers.yaml triggers, effective secrets,\n" +
			"and the account's default CI runner pool.",
		Args: maxArgs(1, "gs policy [folder] [--ref <ref>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			folder := ""
			if len(args) == 1 {
				folder = args[0]
			}
			return r.runFolderPolicy(cmd.Context(), *opts, folder, ref)
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "read policy definitions at this ref (default refs/global/main)")
	withSchemaOutput[folderPolicyOutput](cmd)
	return cmd
}

func (r Runner) runFolderPolicy(ctx context.Context, opts commandOptions, rawFolder, ref string) error {
	folder, err := r.triggerFolder(rawFolder)
	if err != nil {
		return err
	}
	var resp *corev1.FolderPolicyResponse
	if err := r.withSession(ctx, func(s *Session) error {
		var err error
		resp, err = s.Trigger().GetFolderPolicy(s.Ctx(), &corev1.GetFolderPolicyRequest{
			Path: folder,
			Ref:  strings.TrimSpace(ref),
		})
		return triggerError(err)
	}); err != nil {
		return err
	}
	out := folderPolicyFromProto(resp)
	return writeSchemaOutput(r, opts, out, func() error {
		return r.printFolderPolicy(out)
	})
}

func folderPolicyFromProto(resp *corev1.FolderPolicyResponse) folderPolicyOutput {
	out := folderPolicyOutput{
		Folder:            resp.GetFolder(),
		Account:           resp.GetAccount(),
		CommitID:          resp.GetCommitId(),
		OwnerConfigPath:   resp.GetOwnerConfigPath(),
		OwnerSourceFolder: resp.GetOwnerSourceFolder(),
		CanManageSecrets:  resp.GetCanManageSecrets(),
		CIDaemonIDs:       append([]string(nil), resp.GetCiDaemonIds()...),
		CIDaemonNames:     append([]string(nil), resp.GetCiDaemonNames()...),
	}
	for _, rule := range resp.GetOwnerRules() {
		out.OwnerRules = append(out.OwnerRules, folderOwnerRuleOutput{
			Paths:        append([]string(nil), rule.GetPaths()...),
			Owners:       append([]string(nil), rule.GetOwners()...),
			Require:      rule.GetRequire(),
			RequireHuman: rule.GetRequireHuman(),
		})
	}
	for _, chk := range resp.GetChecks() {
		out.Checks = append(out.Checks, folderCheckOutput{
			ID:         chk.GetId(),
			Name:       chk.GetName(),
			Folder:     chk.GetFolder(),
			ConfigPath: chk.GetConfigPath(),
			Run:        chk.GetRun(),
			Paths:      append([]string(nil), chk.GetPaths()...),
			StartedBy:  chk.GetStartedBy(),
			Required:   chk.GetRequired(),
		})
	}
	for _, t := range resp.GetTriggers() {
		out.Triggers = append(out.Triggers, triggerOutput{
			ID:          t.GetId(),
			Folder:      t.GetFolder(),
			On:          append([]string(nil), t.GetOn()...),
			Paths:       append([]string(nil), t.GetPaths()...),
			Changes:     append([]string(nil), t.GetChanges()...),
			Action:      t.GetAction(),
			Worker:      t.GetWorker(),
			URL:         t.GetHttpUrl(),
			Checks:      append([]string(nil), t.GetChecks()...),
			Secrets:     append([]string(nil), t.GetSecrets()...),
			Network:     append([]string(nil), t.GetNetwork()...),
			Concurrency: t.GetConcurrency(),
		})
	}
	for _, sec := range resp.GetSecrets() {
		out.Secrets = append(out.Secrets, folderSecretOutput{
			Name:         sec.GetName(),
			SecretID:     sec.GetSecretId(),
			SourceFolder: sec.GetSourceFolder(),
			Paths:        append([]string(nil), sec.GetPaths()...),
		})
	}
	for _, p := range resp.GetProblems() {
		out.Problems = append(out.Problems, folderPolicyProblemOutput{
			Kind:    p.GetKind(),
			Folder:  p.GetFolder(),
			File:    p.GetFile(),
			Message: p.GetMessage(),
		})
	}
	return out
}

func (r Runner) printFolderPolicy(out folderPolicyOutput) error {
	fmt.Fprintf(r.Stdout, "folder: %s\n", out.Folder)
	if out.CommitID != "" {
		fmt.Fprintf(r.Stdout, "commit: %s\n", out.CommitID)
	}

	fmt.Fprintln(r.Stdout, "\nOwners:")
	if len(out.OwnerRules) == 0 {
		fmt.Fprintln(r.Stdout, "  (none)")
	} else {
		if out.OwnerConfigPath != "" {
			fmt.Fprintf(r.Stdout, "  source: %s\n", out.OwnerConfigPath)
		}
		for _, rule := range out.OwnerRules {
			gate := fmt.Sprintf("require=%d", rule.Require)
			if rule.RequireHuman > 0 {
				gate += fmt.Sprintf(" require_human=%d", rule.RequireHuman)
			}
			fmt.Fprintf(r.Stdout, "  %s\t%s\t(%s)\n", strings.Join(rule.Paths, ","), strings.Join(rule.Owners, ","), gate)
		}
	}

	fmt.Fprintln(r.Stdout, "\nChecks:")
	if len(out.Checks) == 0 {
		fmt.Fprintln(r.Stdout, "  (none)")
	} else {
		for _, chk := range out.Checks {
			tags := []string{}
			if chk.Required {
				tags = append(tags, "required")
			}
			if chk.StartedBy == "trigger" {
				tags = append(tags, "trigger-started")
			}
			if chk.Folder != "" && chk.Folder != out.Folder {
				tags = append(tags, "from "+chk.Folder)
			}
			suffix := ""
			if len(tags) > 0 {
				suffix = " [" + strings.Join(tags, ", ") + "]"
			}
			fmt.Fprintf(r.Stdout, "  %s%s\t%s\n", chk.ID, suffix, chk.Run)
		}
	}

	fmt.Fprintln(r.Stdout, "\nTriggers:")
	if len(out.Triggers) == 0 {
		fmt.Fprintln(r.Stdout, "  (none)")
	} else {
		for _, t := range out.Triggers {
			what := t.Worker
			if t.Action == "http" {
				what = t.URL
			} else if t.Action == "check" {
				what = strings.Join(t.Checks, ",")
			}
			fmt.Fprintf(r.Stdout, "  %s\ton %s\t%s %s\n", t.ID, strings.Join(t.On, ","), t.Action, what)
		}
	}

	fmt.Fprintln(r.Stdout, "\nSecrets:")
	if len(out.Secrets) == 0 {
		fmt.Fprintln(r.Stdout, "  (none)")
	} else {
		for _, sec := range out.Secrets {
			if sec.SourceFolder != "" {
				origin := "defined here"
				if sec.SourceFolder != out.Folder {
					origin = "inherited from " + sec.SourceFolder
				}
				fmt.Fprintf(r.Stdout, "  %s\t(%s)\n", sec.Name, origin)
			} else {
				fmt.Fprintf(r.Stdout, "  %s\n", sec.Name)
			}
		}
	}

	fmt.Fprintln(r.Stdout, "\nCI Runners:")
	if len(out.CIDaemonIDs) == 0 {
		fmt.Fprintln(r.Stdout, "  (none configured on account)")
	} else {
		for i, id := range out.CIDaemonIDs {
			name := ""
			if i < len(out.CIDaemonNames) {
				name = out.CIDaemonNames[i]
			}
			if name != "" && name != id {
				fmt.Fprintf(r.Stdout, "  %d. %s (%s)\n", i+1, name, id)
			} else {
				fmt.Fprintf(r.Stdout, "  %d. %s\n", i+1, id)
			}
		}
	}

	for _, p := range out.Problems {
		fmt.Fprintf(r.stderr(), "problem (%s) %s: %s\n", p.Kind, p.File, p.Message)
	}
	return nil
}
