package configcmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/cli/clibase"
	"gitslice.io/gitslice/internal/workspace"
)

// Output is the structured JSON representation of `gs config list` / `gs config set`.
type Output = output

type output struct {
	ConfigPath   string `json:"config_path"`
	ServerAddr   string `json:"server_addr,omitempty"`
	SubjectID    string `json:"subject_id,omitempty"`
	TokenPresent bool   `json:"token_present"`
}

// NewCommand builds the `gs config` command group.
func NewCommand(app clibase.App, opts *clibase.Options) *cobra.Command {
	configCmd := &cobra.Command{
		Use:     "config",
		Aliases: []string{"cfg"},
		Short:   "Manage local CLI configuration",
		RunE:    clibase.RequireSubcommand("config"),
	}
	clibase.MarkSchemaGroup(configCmd)
	configListCmd := &cobra.Command{
		Use:   "list",
		Short: "List local CLI configuration",
		Args:  clibase.NoArgs("gs config list"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(app, *opts)
		},
	}
	configGetCmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Get one local CLI configuration value",
		Args:  clibase.ExactArgs(1, "gs config get <key>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGet(app, *opts, args[0])
		},
	}
	configSetCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set one local CLI configuration value",
		Args:  clibase.ExactArgs(2, "gs config set <key> <value>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSet(app, *opts, args[0], args[1])
		},
	}
	configCmd.AddCommand(configListCmd, configGetCmd, configSetCmd)
	return configCmd
}

func runList(app clibase.App, opts clibase.Options) error {
	out, err := buildOutput(app)
	if err != nil {
		return err
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(app.Stdout(), "config_path: %s\n", out.ConfigPath)
	if out.ServerAddr != "" {
		fmt.Fprintf(app.Stdout(), "server_addr: %s\n", out.ServerAddr)
	}
	if out.SubjectID != "" {
		fmt.Fprintf(app.Stdout(), "subject_id: %s\n", out.SubjectID)
	}
	fmt.Fprintf(app.Stdout(), "token_present: %t\n", out.TokenPresent)
	return nil
}

func runGet(app clibase.App, opts clibase.Options, key string) error {
	out, err := buildOutput(app)
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	var value any
	switch key {
	case "config_path":
		value = out.ConfigPath
	case "server_addr":
		value = out.ServerAddr
	case "subject_id":
		value = out.SubjectID
	case "token_present":
		value = out.TokenPresent
	case "token":
		return clibase.UserError("secret_config_key", "token is secret and cannot be printed by config", "Run gs auth token only when a script needs the bearer token, or gs auth status for non-secret auth state.")
	default:
		return clibase.UserError("unknown_config_key", "unknown config key "+key, "Supported keys: config_path, server_addr, subject_id, token_present.")
	}
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, map[string]any{key: value})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(app.Stdout(), "%v\n", value)
	return nil
}

func runSet(app clibase.App, opts clibase.Options, key, value string) error {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if value == "" {
		return clibase.UserError("invalid_config_value", "config value cannot be empty", "Pass a non-empty value.")
	}
	cfg, err := app.ReadPartialUserConfig()
	if err != nil {
		return err
	}
	switch key {
	case "server_addr":
		cfg.ServerAddr = value
	case "subject_id", "token", "token_present", "config_path":
		return clibase.UserError("readonly_config_key", "config key "+key+" is read-only", "Use gs auth login or gs auth signup to update authentication state.")
	default:
		return clibase.UserError("unknown_config_key", "unknown config key "+key, "Only server_addr can be set directly.")
	}
	if err := app.WriteUserConfig(cfg); err != nil {
		return err
	}
	out := outputFromUserConfig(app.UserConfigPath(), cfg)
	if opts.JSONOutput() {
		return app.WriteJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(app.Stdout(), "set %s\n", key)
	return nil
}

func buildOutput(app clibase.App) (output, error) {
	cfg, err := app.ReadPartialUserConfig()
	if err != nil {
		return output{}, err
	}
	return outputFromUserConfig(app.UserConfigPath(), cfg), nil
}

func outputFromUserConfig(configPath string, cfg workspace.UserConfig) output {
	return output{
		ConfigPath:   configPath,
		ServerAddr:   cfg.ServerAddr,
		SubjectID:    cfg.SubjectID,
		TokenPresent: cfg.Token != "",
	}
}
