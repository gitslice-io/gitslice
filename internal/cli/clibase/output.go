package clibase

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/template"

	"github.com/itchyny/gojq"
)

func WantsJSON(args []string) bool {
	for i, arg := range args {
		if arg == "--json" {
			return true
		}
		if strings.HasPrefix(arg, "--json=") {
			return true
		}
		if arg == "--jq" {
			return true
		}
		if strings.HasPrefix(arg, "--jq=") {
			return true
		}
		if arg == "--template" {
			return true
		}
		if strings.HasPrefix(arg, "--template=") {
			return true
		}
		if arg == "--format=json" {
			return true
		}
		if arg == "--format" && i+1 < len(args) && args[i+1] == "json" {
			return true
		}
	}
	return false
}

func ParseJSONFields(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return nil, nil
	}
	fields := strings.Split(raw, ",")
	out := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			return nil, UserError("invalid_json_fields", "empty JSON field selector", "Use --json=field or --json=field,field.")
		}
		for _, ch := range field {
			if !(ch == '_' || ch == '-' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z') {
				return nil, UserError("invalid_json_fields", "invalid JSON field selector "+field, "Use field names from gs schema.")
			}
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out, nil
}

func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func WriteJSONOutput(w io.Writer, opts Options, v any) error {
	output := v
	if len(opts.JSONFields) > 0 {
		projected, err := SelectJSONFields(v, opts.JSONFields)
		if err != nil {
			return err
		}
		output = projected
	}
	if opts.JQ != "" {
		return WriteJQOutput(w, opts.JQ, output)
	}
	if opts.Template != "" {
		return WriteTemplateOutput(w, opts.Template, output)
	}
	return WriteJSON(w, output)
}

// WriteSchemaOutput renders val as structured output when --json/--jq/--template
// is active, suppresses human text when --quiet is set, and otherwise invokes
// textFn.
func WriteSchemaOutput[T any](w io.Writer, opts Options, val T, textFn func() error) error {
	if opts.JSONOutput() {
		return WriteJSONOutput(w, opts, val)
	}
	if opts.Quiet || textFn == nil {
		return nil
	}
	return textFn()
}

func WriteJQOutput(w io.Writer, rawQuery string, v any) error {
	data, err := templateData(v)
	if err != nil {
		return err
	}
	query, err := gojq.Parse(rawQuery)
	if err != nil {
		return UserError("invalid_jq", "invalid jq expression: "+err.Error(), "Use jq syntax over fields from gs schema.")
	}
	iter := query.Run(data)
	for {
		value, ok := iter.Next()
		if !ok {
			return nil
		}
		if err, ok := value.(error); ok {
			return UserError("jq_failed", "jq execution failed: "+err.Error(), "Check field names and value types, or run the command with --json.")
		}
		if err := writeJQValue(w, value); err != nil {
			return err
		}
	}
}

func writeJQValue(w io.Writer, value any) error {
	switch v := value.(type) {
	case string:
		_, err := fmt.Fprintln(w, v)
		return err
	case nil:
		_, err := fmt.Fprintln(w, "null")
		return err
	case bool:
		_, err := fmt.Fprintln(w, v)
		return err
	case float64:
		_, err := fmt.Fprintln(w, v)
		return err
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
}

func WriteTemplateOutput(w io.Writer, rawTemplate string, v any) error {
	data, err := templateData(v)
	if err != nil {
		return err
	}
	tpl, err := template.New("gs").Option("missingkey=error").Funcs(template.FuncMap{
		"json": func(v any) (string, error) {
			data, err := json.Marshal(v)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}).Parse(rawTemplate)
	if err != nil {
		return UserError("invalid_template", "invalid template: "+err.Error(), "Use Go text/template syntax over fields from gs schema.")
	}
	if err := tpl.Execute(w, data); err != nil {
		return UserError("template_failed", "template execution failed: "+err.Error(), "Check field names with gs schema or run the command with --json.")
	}
	return nil
}

func templateData(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func SelectJSONFields(v any, fields []string) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, UserError("invalid_json_fields", "JSON field selection requires an object output", "Run without field selection to inspect the full JSON output.")
	}
	out := make(map[string]any, len(fields))
	for _, field := range fields {
		value, ok := obj[field]
		if !ok {
			return nil, UserError("unknown_json_field", "unknown JSON field "+field, "Run the command with --json to inspect available fields or use gs schema.")
		}
		out[field] = value
	}
	return out, nil
}
