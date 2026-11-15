package cli

func (r Runner) runSchema(opts commandOptions) error {
	return r.writeJSONOutput(opts, map[string]any{
		"schema_version": "v1",
		"global_flags": []map[string]any{
			{"name": "--format", "values": []string{"text", "json", "medium"}, "default": "text", "description": "output format"},
			{"name": "--json", "description": "emit JSON output; optional comma-separated fields use --json=field,field"},
			{"name": "--jq", "description": "filter structured output with a jq expression"},
			{"name": "--template", "description": "format structured output with a Go template over JSON fields"},
			{"name": "--quiet", "description": "suppress non-essential text output"},
			{"name": "--non-interactive", "description": "fail instead of prompting for input"},
			{"name": "--no-color", "description": "disable colorized output"},
			{"name": "--verbose", "description": "emit additional diagnostics"},
			{"name": "--debug", "description": "emit debug diagnostics"},
			{"name": "--trace", "description": "emit trace diagnostics"},
		},
		"commands":    schemaCommands(),
		"help_topics": cliHelpTopicSchema(),
		"error_output": map[string]any{
			"stream": "stderr",
			"shape": map[string]any{
				"error": map[string]string{
					"code":      "stable snake_case error code",
					"message":   "human-readable message",
					"hint":      "optional next action",
					"retriable": "boolean",
				},
			},
		},
	})
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

// schemaCommands lists the commands gs schema describes, area by area.
func schemaCommands() []map[string]any {
	var commands []map[string]any
	for _, area := range [][]map[string]any{
		schemaSetupCommands(),
		schemaHistoryCommands(),
		schemaWorkspaceCommands(),
		schemaChangesetCommands(),
		schemaSliceCommands(),
		schemaFileCommands(),
		schemaToolCommands(),
	} {
		commands = append(commands, area...)
	}
	return commands
}
