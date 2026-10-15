package cli

import (
	"fmt"
	"strings"
)

type configOutput struct {
	ConfigPath   string `json:"config_path"`
	ServerAddr   string `json:"server_addr,omitempty"`
	SubjectID    string `json:"subject_id,omitempty"`
	TokenPresent bool   `json:"token_present"`
}

func (r Runner) runConfigList(opts commandOptions) error {
	out, err := r.configOutput()
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "config_path: %s\n", out.ConfigPath)
	if out.ServerAddr != "" {
		fmt.Fprintf(r.Stdout, "server_addr: %s\n", out.ServerAddr)
	}
	if out.SubjectID != "" {
		fmt.Fprintf(r.Stdout, "subject_id: %s\n", out.SubjectID)
	}
	fmt.Fprintf(r.Stdout, "token_present: %t\n", out.TokenPresent)
	return nil
}

func (r Runner) runConfigGet(opts commandOptions, key string) error {
	out, err := r.configOutput()
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
		return userError("secret_config_key", "token is secret and cannot be printed by config", "Run gs auth token only when a script needs the bearer token, or gs auth status for non-secret auth state.")
	default:
		return userError("unknown_config_key", "unknown config key "+key, "Supported keys: config_path, server_addr, subject_id, token_present.")
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{key: value})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "%v\n", value)
	return nil
}

func (r Runner) runConfigSet(opts commandOptions, key, value string) error {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if value == "" {
		return userError("invalid_config_value", "config value cannot be empty", "Pass a non-empty value.")
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return err
	}
	switch key {
	case "server_addr":
		cfg.ServerAddr = value
	case "subject_id", "token", "token_present", "config_path":
		return userError("readonly_config_key", "config key "+key+" is read-only", "Use gs auth login or gs auth signup to update authentication state.")
	default:
		return userError("unknown_config_key", "unknown config key "+key, "Only server_addr can be set directly.")
	}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	out := configOutputFromUserConfig(r.userConfigPath(), cfg)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "set %s\n", key)
	return nil
}

func (r Runner) configOutput() (configOutput, error) {
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return configOutput{}, err
	}
	return configOutputFromUserConfig(r.userConfigPath(), cfg), nil
}

func configOutputFromUserConfig(configPath string, cfg UserConfig) configOutput {
	return configOutput{
		ConfigPath:   configPath,
		ServerAddr:   cfg.ServerAddr,
		SubjectID:    cfg.SubjectID,
		TokenPresent: cfg.Token != "",
	}
}
