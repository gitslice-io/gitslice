package cli

import (
	"reflect"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	schemaGroupAnnotation      = "gitslice.io/schema-group"
	schemaExcludedAnnotation   = "gitslice.io/schema-excluded"
	schemaCollapsedAnnotation  = "gitslice.io/schema-collapsed"
	schemaUseAnnotation        = "gitslice.io/schema-use"
	schemaSummaryAnnotation    = "gitslice.io/schema-summary"
	schemaOutputAnnotation     = "gitslice.io/schema-output"
	schemaOutputSetAnnotation  = "gitslice.io/schema-output-set"
	schemaOutputTypeAnnotation = "gitslice.io/schema-output-type"
)

var (
	schemaOutputTypesMu sync.RWMutex
	schemaOutputTypes   = map[string]reflect.Type{}
)

func registerSchemaOutputType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	key := t.String()
	if t.PkgPath() != "" && t.Name() != "" {
		key = t.PkgPath() + "." + t.Name()
	}
	schemaOutputTypesMu.Lock()
	schemaOutputTypes[key] = t
	schemaOutputTypesMu.Unlock()
	return key
}

func schemaOutputTypeForCommand(cmd *cobra.Command) (reflect.Type, bool) {
	if cmd == nil {
		return nil, false
	}
	key := cmd.Annotations[schemaOutputTypeAnnotation]
	if key == "" {
		return nil, false
	}
	schemaOutputTypesMu.RLock()
	t, ok := schemaOutputTypes[key]
	schemaOutputTypesMu.RUnlock()
	return t, ok
}

func ensureCommandAnnotations(cmd *cobra.Command) map[string]string {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	return cmd.Annotations
}

func markSchemaGroup(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		if cmd != nil {
			ensureCommandAnnotations(cmd)[schemaGroupAnnotation] = "true"
		}
	}
}

func markSchemaExcluded(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		if cmd != nil {
			ensureCommandAnnotations(cmd)[schemaExcludedAnnotation] = "true"
		}
	}
}

func withSchemaCollapsed(cmd *cobra.Command, use, summary string) {
	if cmd == nil {
		return
	}
	ann := ensureCommandAnnotations(cmd)
	ann[schemaCollapsedAnnotation] = "true"
	if use != "" {
		ann[schemaUseAnnotation] = use
	}
	if summary != "" {
		ann[schemaSummaryAnnotation] = summary
	}
}

func withSchemaOutput[T any](cmds ...*cobra.Command) {
	t := reflect.TypeFor[T]()
	typeKey := registerSchemaOutputType(t)
	fields := structJSONFields(t)
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		withSchemaOutputFields(cmd, fields...)
		ensureCommandAnnotations(cmd)[schemaOutputTypeAnnotation] = typeKey
	}
}

func withSchemaOutputFields(cmd *cobra.Command, fields ...string) {
	if cmd == nil {
		return
	}
	ann := ensureCommandAnnotations(cmd)
	ann[schemaOutputSetAnnotation] = "true"
	ann[schemaOutputAnnotation] = strings.Join(fields, ",")
}

func structJSONFields(t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	var visit func(reflect.Type)
	visit = func(cur reflect.Type) {
		for cur.Kind() == reflect.Pointer {
			cur = cur.Elem()
		}
		if cur.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < cur.NumField(); i++ {
			field := cur.Field(i)
			tag := field.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if field.Anonymous && name == "" {
				visit(field.Type)
				continue
			}
			if !field.IsExported() {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	visit(t)
	return out
}

func (r Runner) runSchema(opts commandOptions, filter []string) error {
	root := r.rootCommand()
	commands := schemaCommandsFromRoot(root)
	if len(filter) > 0 {
		commands = filterSchemaCommands(commands, filter)
		if len(commands) == 0 {
			return userError(
				"not_found",
				"no command matching "+strings.Join(filter, " "),
				"Run gs schema to inspect all available commands.",
			)
		}
	}
	payload := map[string]any{
		"schema_version": "v1",
		"global_flags":   schemaGlobalFlags(root),
		"commands":       commands,
		"help_topics":    cliHelpTopicSchema(),
		"error_output": map[string]any{
			"stream": "stderr",
			"shape": map[string]any{
				"error": map[string]string{
					"code":                "stable snake_case error code",
					"message":             "human-readable message",
					"hint":                "optional next action",
					"remediation_command": "optional suggested CLI command to recover",
					"remediation_url":     "optional web URL to recover",
					"retriable":           "boolean",
				},
			},
		},
	}
	if len(filter) > 0 {
		payload["filter"] = strings.Join(filter, " ")
	}
	return r.writeJSONOutput(opts, payload)
}

func filterSchemaCommands(commands []map[string]any, filter []string) []map[string]any {
	tokens := make([]string, 0, len(filter))
	for _, f := range filter {
		for _, part := range strings.Fields(strings.ToLower(strings.TrimSpace(f))) {
			tokens = append(tokens, part)
		}
	}
	if len(tokens) > 0 && tokens[0] == "gs" {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return commands
	}
	prefix := "gs " + strings.Join(tokens, " ")
	matchesPath := func(use string) bool {
		// Strip argument placeholders after command tokens by taking leading word tokens.
		lower := strings.ToLower(strings.TrimSpace(use))
		if lower == prefix || strings.HasPrefix(lower, prefix+" ") {
			return true
		}
		return false
	}
	var out []map[string]any
	for _, entry := range commands {
		use, _ := entry["use"].(string)
		if matchesPath(use) {
			out = append(out, entry)
			continue
		}
		if aliases, ok := entry["aliases"].([]string); ok {
			for _, a := range aliases {
				if matchesPath(a) {
					out = append(out, entry)
					break
				}
			}
		}
	}
	return out
}

func schemaGlobalFlags(root *cobra.Command) []map[string]any {
	var flags []map[string]any
	root.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || flag.Name == "help" {
			return
		}
		entry := map[string]any{
			"name":        "--" + flag.Name,
			"description": flag.Usage,
		}
		if flag.Shorthand != "" && flag.ShorthandDeprecated == "" {
			entry["shorthand"] = "-" + flag.Shorthand
		}
		if flag.Name == "format" {
			entry["values"] = []string{"text", "json", "medium"}
			entry["default"] = flag.DefValue
		}
		flags = append(flags, entry)
	})
	return flags
}

func cliHelpTopicSchema() []map[string]string {
	out := make([]map[string]string, 0, len(cliHelpTopics))
	for _, topic := range cliHelpTopics {
		out = append(out, map[string]string{
			"name":    topic.Name,
			"summary": topic.Summary,
		})
	}
	return out
}

func isSchemaGroupCommand(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if cmd.Annotations[schemaCollapsedAnnotation] == "true" {
		return false
	}
	if cmd.Annotations[schemaGroupAnnotation] == "true" {
		return true
	}
	return !cmd.Runnable()
}

func isSchemaExcludedCommand(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Annotations[schemaExcludedAnnotation] == "true"
}

func isSchemaCollapsedCommand(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Annotations[schemaCollapsedAnnotation] == "true"
}

func schemaCommandsFromRoot(root *cobra.Command) []map[string]any {
	var commands []map[string]any
	var walk func(ancestors []*cobra.Command, cmd *cobra.Command)
	walk = func(ancestors []*cobra.Command, cmd *cobra.Command) {
		if cmd == nil || cmd.Hidden || isSchemaExcludedCommand(cmd) {
			return
		}
		chain := append(append([]*cobra.Command(nil), ancestors...), cmd)
		if !isSchemaGroupCommand(cmd) {
			commands = append(commands, buildSchemaCommandEntry(root, chain))
		}
		if isSchemaCollapsedCommand(cmd) {
			return
		}
		for _, child := range cmd.Commands() {
			walk(chain, child)
		}
	}
	for _, child := range root.Commands() {
		walk(nil, child)
	}
	return commands
}

func buildSchemaCommandEntry(root *cobra.Command, chain []*cobra.Command) map[string]any {
	cmd := chain[len(chain)-1]
	use := schemaCommandUse(root, chain)
	summary := strings.TrimSpace(cmd.Short)
	if override := strings.TrimSpace(cmd.Annotations[schemaSummaryAnnotation]); override != "" {
		summary = override
	}
	entry := map[string]any{
		"use":           use,
		"summary":       summary,
		"writes_stdout": true,
	}
	if aliases := schemaCommandAliases(root, chain); len(aliases) > 0 {
		entry["aliases"] = aliases
	}
	if args := parseSchemaCommandArgs(cmd, use); len(args) > 0 {
		entry["args"] = args
	}
	if flags := collectSchemaCommandFlags(chain); len(flags) > 0 {
		entry["flags"] = flags
	}
	if output, ok := resolveSchemaMachineOutput(root, chain, cmd); ok {
		entry["machine_output"] = output
	}
	return entry
}

func schemaCommandCanonicalPath(root *cobra.Command, chain []*cobra.Command) string {
	parts := make([]string, 0, len(chain)+1)
	parts = append(parts, root.Name())
	for _, c := range chain {
		parts = append(parts, c.Name())
	}
	return strings.Join(parts, " ")
}

func schemaCommandUse(root *cobra.Command, chain []*cobra.Command) string {
	cmd := chain[len(chain)-1]
	if override := strings.TrimSpace(cmd.Annotations[schemaUseAnnotation]); override != "" {
		return override
	}
	parts := make([]string, 0, len(chain)+1)
	parts = append(parts, root.Name())
	for i := 0; i < len(chain)-1; i++ {
		parts = append(parts, chain[i].Name())
	}
	parts = append(parts, strings.TrimSpace(cmd.Use))
	return strings.Join(parts, " ")
}

func schemaCommandAliases(root *cobra.Command, chain []*cobra.Command) []string {
	cmd := chain[len(chain)-1]
	if isSchemaCollapsedCommand(cmd) {
		return nil
	}
	tail := strings.TrimPrefix(strings.TrimSpace(cmd.Use), cmd.Name())
	tuples := [][]string{{root.Name()}}
	for _, c := range chain {
		names := append([]string{c.Name()}, c.Aliases...)
		next := make([][]string, 0, len(tuples)*len(names))
		for _, prefix := range tuples {
			for _, name := range names {
				row := make([]string, 0, len(prefix)+1)
				row = append(row, prefix...)
				row = append(row, name)
				next = append(next, row)
			}
		}
		tuples = next
	}
	if len(tuples) <= 1 {
		return nil
	}
	aliases := make([]string, 0, len(tuples)-1)
	for _, tuple := range tuples[1:] {
		aliases = append(aliases, strings.Join(tuple, " ")+tail)
	}
	return aliases
}

func collectSchemaCommandFlags(chain []*cobra.Command) []string {
	var out []string
	seen := make(map[string]bool)
	visit := func(fs *pflag.FlagSet) {
		fs.VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden || flag.Name == "help" {
				return
			}
			longName := "--" + flag.Name
			if !seen[longName] {
				seen[longName] = true
				out = append(out, longName)
			}
			if flag.Shorthand != "" && flag.ShorthandDeprecated == "" {
				shortName := "-" + flag.Shorthand
				if !seen[shortName] {
					seen[shortName] = true
					out = append(out, shortName)
				}
			}
		})
	}
	for i := 0; i < len(chain)-1; i++ {
		visit(chain[i].PersistentFlags())
	}
	last := chain[len(chain)-1]
	visit(last.PersistentFlags())
	visit(last.Flags())
	return out
}
