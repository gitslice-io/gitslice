package cli

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed skill_embed.md
var embeddedSkillMarkdown string

type skillInstallResult struct {
	Skill     string   `json:"skill"`
	Installed []string `json:"installed"`
}

func (r Runner) skillCommand(opts *commandOptions) *cobra.Command {
	skillCmd := &cobra.Command{
		Use:   "skill",
		Short: "Install or print the official Gitslice Agent Skill (SKILL.md)",
		RunE:  requireSubcommand("skill"),
	}
	markSchemaGroup(skillCmd)

	printCmd := &cobra.Command{
		Use:   "print",
		Short: "Print the canonical Gitslice SKILL.md to stdout",
		Args:  noArgs("gs skill print"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.jsonOutput() {
				return r.writeJSONOutput(*opts, map[string]any{
					"name":    "gitslice",
					"content": embeddedSkillMarkdown,
				})
			}
			_, err := fmt.Fprint(r.Stdout, embeddedSkillMarkdown)
			return err
		},
	}

	target := "auto"
	project := false
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install the Gitslice Agent Skill into detected coding agent directories",
		Long: `Install the canonical Gitslice SKILL.md so coding agents (Claude Code,
Codex, Cursor, Gemini/Antigravity, or any Agent Skills harness) know gs
workflows immediately.

With --target auto (default), gs detects existing user-level skill roots
(~/.agents, ~/.claude, ~/.gemini, ~/.cursor, ~/.codex) and installs into each;
if none exist, it installs into ~/.agents/skills/gitslice/SKILL.md. Pass
--project to install into the current directory instead.`,
		Args: noArgs("gs skill install [--target auto|agents|claude|gemini|cursor|codex|DIR] [--project]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := r.installSkill(target, project)
			if err != nil {
				return err
			}
			if opts.jsonOutput() {
				return r.writeJSONOutput(*opts, res)
			}
			if opts.Quiet {
				return nil
			}
			for _, p := range res.Installed {
				fmt.Fprintf(r.Stdout, "installed gitslice skill to %s\n", p)
			}
			return nil
		},
	}
	installCmd.Flags().StringVar(&target, "target", target, "harness target: auto, agents, claude, gemini, cursor, codex, or a directory path")
	installCmd.Flags().BoolVar(&project, "project", project, "install into the current project directory instead of user home")
	withSchemaOutput[skillInstallResult](installCmd)

	skillCmd.AddCommand(printCmd, installCmd)
	return skillCmd
}

func (r Runner) installSkill(target string, project bool) (skillInstallResult, error) {
	dirs, err := r.resolveSkillTargetDirs(strings.TrimSpace(target), project)
	if err != nil {
		return skillInstallResult{}, err
	}
	installed := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		destDir := filepath.Join(dir, "gitslice")
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return skillInstallResult{}, err
		}
		destFile := filepath.Join(destDir, "SKILL.md")
		if err := os.WriteFile(destFile, []byte(embeddedSkillMarkdown), 0o644); err != nil {
			return skillInstallResult{}, err
		}
		installed = append(installed, destFile)
	}
	return skillInstallResult{Skill: "gitslice", Installed: installed}, nil
}

func (r Runner) resolveSkillTargetDirs(target string, project bool) ([]string, error) {
	base := r.homeDir()
	if project {
		base = r.cwd()
	}
	if base == "" {
		var err error
		if project {
			base, err = os.Getwd()
		} else {
			base, err = os.UserHomeDir()
		}
		if err != nil {
			return nil, err
		}
	}

	known := map[string]string{
		"agents": filepath.Join(base, ".agents", "skills"),
		"claude": filepath.Join(base, ".claude", "skills"),
		"gemini": filepath.Join(base, ".gemini", "skills"),
		"cursor": filepath.Join(base, ".cursor", "skills"),
		"codex":  filepath.Join(base, ".codex", "skills"),
	}

	lower := strings.ToLower(target)
	if lower == "" || lower == "auto" {
		if project {
			return []string{known["agents"]}, nil
		}
		var found []string
		for _, key := range []string{"agents", "claude", "gemini", "cursor", "codex"} {
			root := filepath.Dir(known[key])
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				found = append(found, known[key])
			}
		}
		if len(found) == 0 {
			found = []string{known["agents"]}
		}
		return found, nil
	}

	if dir, ok := known[lower]; ok {
		return []string{dir}, nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(r.cwd(), target)
	}
	return []string{target}, nil
}
