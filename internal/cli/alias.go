package cli

import (
	"fmt"
	"sort"
	"strings"
)

type aliasEntryOutput struct {
	Name      string `json:"name"`
	Expansion string `json:"expansion"`
}

func (r Runner) expandAliasArgs(args []string) ([]string, error) {
	idx := aliasExpansionIndex(args)
	if idx < 0 {
		return args, nil
	}
	name := args[idx]
	if name == "alias" || name == "help" || name == "completion" {
		return args, nil
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return nil, err
	}
	expansion, ok := cfg.Aliases[name]
	if !ok {
		return args, nil
	}
	parts, err := splitAliasExpansion(expansion)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, userError("invalid_alias", "alias "+name+" has an empty expansion", "Run gs alias set "+name+" '<command>' to replace it.")
	}
	expanded := make([]string, 0, len(args)-1+len(parts))
	expanded = append(expanded, args[:idx]...)
	expanded = append(expanded, parts...)
	expanded = append(expanded, args[idx+1:]...)
	return expanded, nil
}

func aliasExpansionIndex(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				return i + 1
			}
			return -1
		}
		if !strings.HasPrefix(arg, "-") {
			return i
		}
		switch {
		case arg == "--format" || arg == "--json" || arg == "--jq" || arg == "--template":
			i++
		case strings.HasPrefix(arg, "--format=") || strings.HasPrefix(arg, "--json=") || strings.HasPrefix(arg, "--jq=") || strings.HasPrefix(arg, "--template="):
		case arg == "--quiet" || arg == "--non-interactive" || arg == "--no-color" || arg == "--verbose" || arg == "--debug" || arg == "--trace":
		default:
			return i
		}
	}
	return -1
}

func (r Runner) runAliasList(opts commandOptions) error {
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return err
	}
	entries := aliasEntries(cfg.Aliases)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"aliases": entries})
	}
	if opts.Quiet {
		return nil
	}
	if len(entries) == 0 {
		fmt.Fprintln(r.Stdout, "no aliases configured")
		return nil
	}
	for _, entry := range entries {
		fmt.Fprintf(r.Stdout, "%s: %s\n", entry.Name, entry.Expansion)
	}
	return nil
}

func (r Runner) runAliasSet(opts commandOptions, name, expansion string) error {
	name = strings.TrimSpace(name)
	expansion = strings.TrimSpace(expansion)
	if err := validateAliasName(name); err != nil {
		return err
	}
	if reservedAliasNames()[name] {
		return userError("reserved_alias", "alias "+name+" conflicts with a built-in command", "Choose a different alias name.")
	}
	if parts, err := splitAliasExpansion(expansion); err != nil {
		return err
	} else if len(parts) == 0 {
		return userError("invalid_alias", "alias expansion cannot be empty", "Pass a command such as 'status --json'.")
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return err
	}
	if cfg.Aliases == nil {
		cfg.Aliases = map[string]string{}
	}
	cfg.Aliases[name] = expansion
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	entry := aliasEntryOutput{Name: name, Expansion: expansion}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, entry)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "set alias %s: %s\n", name, expansion)
	return nil
}

func (r Runner) runAliasDelete(opts commandOptions, name string) error {
	name = strings.TrimSpace(name)
	if err := validateAliasName(name); err != nil {
		return err
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Aliases[name]; !ok {
		return userError("unknown_alias", "unknown alias "+name, "Run gs alias list to inspect configured aliases.")
	}
	delete(cfg.Aliases, name)
	if len(cfg.Aliases) == 0 {
		cfg.Aliases = nil
	}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"name": name, "deleted": true})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "deleted alias %s\n", name)
	return nil
}

func aliasEntries(aliases map[string]string) []aliasEntryOutput {
	entries := make([]aliasEntryOutput, 0, len(aliases))
	for name, expansion := range aliases {
		entries = append(entries, aliasEntryOutput{Name: name, Expansion: expansion})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries
}

func validateAliasName(name string) error {
	if name == "" {
		return userError("invalid_alias", "alias name cannot be empty", "Use letters, numbers, dash, or underscore.")
	}
	if strings.HasPrefix(name, "-") {
		return userError("invalid_alias", "alias name cannot start with '-'", "Use letters, numbers, dash, or underscore.")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return userError("invalid_alias", "alias name contains unsupported characters: "+name, "Use letters, numbers, dash, or underscore.")
	}
	return nil
}

func reservedAliasNames() map[string]bool {
	names := []string{
		"alias", "auth", "browse", "ci", "completion", "config", "cfg", "context", "ctx", "init",
		"cs", "changeset", "diff", "file", "fs", "help", "log", "repo", "repository", "rpc", "schema", "show",
		"shell", "slice", "slices", "st", "status", "version", "workspace", "ws",
	}
	reserved := make(map[string]bool, len(names))
	for _, name := range names {
		reserved[name] = true
	}
	return reserved
}

func splitAliasExpansion(value string) ([]string, error) {
	var parts []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		parts = append(parts, current.String())
		current.Reset()
	}
	for _, r := range strings.TrimSpace(value) {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, userError("invalid_alias", "alias expansion has an unterminated quote", "Close the quote or remove it.")
	}
	flush()
	return parts, nil
}
