package cli

import (
	"reflect"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	schemaGroupAnnotation     = "gitslice.io/schema-group"
	schemaExcludedAnnotation  = "gitslice.io/schema-excluded"
	schemaCollapsedAnnotation = "gitslice.io/schema-collapsed"
	schemaUseAnnotation       = "gitslice.io/schema-use"
	schemaSummaryAnnotation   = "gitslice.io/schema-summary"
	schemaOutputAnnotation    = "gitslice.io/schema-output"
	schemaOutputSetAnnotation = "gitslice.io/schema-output-set"
)

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
	fields := structJSONFields(reflect.TypeFor[T]())
	for _, cmd := range cmds {
		withSchemaOutputFields(cmd, fields...)
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

func (r Runner) runSchema(opts commandOptions) error {
	root := r.rootCommand()
	return r.writeJSONOutput(opts, map[string]any{
		"schema_version": "v1",
		"global_flags":   schemaGlobalFlags(root),
		"commands":       schemaCommandsFromRoot(root),
		"help_topics":    cliHelpTopicSchema(),
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

func lookupSchemaFlag(cmd *cobra.Command, rawFlag string) *pflag.Flag {
	name := strings.TrimLeft(rawFlag, "-")
	if idx := strings.IndexByte(name, '='); idx >= 0 {
		name = name[:idx]
	}
	for cur := cmd; cur != nil; cur = cur.Parent() {
		if f := cur.Flags().Lookup(name); f != nil {
			return f
		}
		if f := cur.PersistentFlags().Lookup(name); f != nil {
			return f
		}
		if len(name) == 1 {
			if f := cur.Flags().ShorthandLookup(name); f != nil {
				return f
			}
			if f := cur.PersistentFlags().ShorthandLookup(name); f != nil {
				return f
			}
		}
	}
	return nil
}

func tokenizeUsageTail(tail string) []string {
	var tokens []string
	var cur strings.Builder
	depthSquare, depthAngle, depthParen := 0, 0, 0
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
	}
	for i := 0; i < len(tail); i++ {
		ch := tail[i]
		switch ch {
		case '[':
			depthSquare++
			cur.WriteByte(ch)
		case ']':
			if depthSquare > 0 {
				depthSquare--
			}
			cur.WriteByte(ch)
		case '<':
			depthAngle++
			cur.WriteByte(ch)
		case '>':
			if depthAngle > 0 {
				depthAngle--
			}
			cur.WriteByte(ch)
		case '(':
			depthParen++
			cur.WriteByte(ch)
		case ')':
			if depthParen > 0 {
				depthParen--
			}
			cur.WriteByte(ch)
		case ' ', '\t':
			if depthSquare == 0 && depthAngle == 0 && depthParen == 0 {
				flush()
			} else {
				cur.WriteByte(ch)
			}
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	return tokens
}

func parseSchemaCommandArgs(cmd *cobra.Command, fullUse string) []string {
	var tail string
	if isSchemaCollapsedCommand(cmd) {
		fields := strings.Fields(fullUse)
		if len(fields) >= 2 {
			tail = strings.TrimSpace(strings.TrimPrefix(fullUse, strings.Join(fields[:2], " ")))
		}
	} else {
		tail = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cmd.Use), cmd.Name()))
	}
	if tail == "" {
		return nil
	}
	tokens := tokenizeUsageTail(tail)
	var out []string
	seen := make(map[string]bool)
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "" || tok == "|" || tok == "--" {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			if f := lookupSchemaFlag(cmd, tok); (f == nil || f.Value.Type() != "bool") && !strings.Contains(tok, "=") && i+1 < len(tokens) {
				next := tokens[i+1]
				if !strings.HasPrefix(next, "-") && !strings.HasPrefix(next, "[") && !strings.HasPrefix(next, "(") && next != "|" {
					i++
				}
			}
			continue
		}
		inner := tok
		for len(inner) >= 2 && ((inner[0] == '[' && inner[len(inner)-1] == ']') || (inner[0] == '(' && inner[len(inner)-1] == ')')) {
			inner = strings.TrimSpace(inner[1 : len(inner)-1])
		}
		if strings.HasPrefix(inner, "-- ") {
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "-- "))
		} else if strings.HasPrefix(inner, "-") {
			continue
		}
		inner = strings.TrimSuffix(inner, "...")
		if len(inner) >= 2 && inner[0] == '<' && inner[len(inner)-1] == '>' {
			inner = strings.TrimSpace(inner[1 : len(inner)-1])
		}
		inner = strings.TrimSuffix(inner, "...")
		inner = strings.TrimSpace(inner)
		if inner != "" && !seen[inner] {
			seen[inner] = true
			out = append(out, inner)
		}
	}
	return out
}

func resolveSchemaMachineOutput(root *cobra.Command, chain []*cobra.Command, cmd *cobra.Command) ([]string, bool) {
	if cmd.Annotations[schemaOutputSetAnnotation] == "true" {
		raw := cmd.Annotations[schemaOutputAnnotation]
		if raw == "" {
			return []string{}, true
		}
		return strings.Split(raw, ","), true
	}
	path := schemaCommandCanonicalPath(root, chain)
	if fields, ok := defaultSchemaCommandOutputs[path]; ok {
		return fields, true
	}
	return nil, false
}

var defaultSchemaCommandOutputs = map[string][]string{
	"gs auth login":            {"server_addr", "subject_id"},
	"gs auth status":           {"signed_in", "server_addr", "subject_id", "accounts", "memberships", "shared_slices", "active_account", "reason"},
	"gs auth token":            {"token", "server_addr", "subject_id"},
	"gs auth logout":           {"signed_in", "was_signed_in", "server_addr", "config_path"},
	"gs auth keys list":        {"account", "agent_subject_id", "keys"},
	"gs auth keys create":      {"account", "agent_subject_id", "key", "secret"},
	"gs auth keys revoke":      {"key"},
	"gs auth use-key":          {"server_addr", "subject_id", "account"},
	"gs agent create":          {"agent_subject_id", "account", "key", "secret", "slice", "role"},
	"gs upgrade":               {"current", "latest", "target", "path", "upgraded", "up_to_date"},
	"gs doctor":                {"ok", "checks"},
	"gs feedback":              {"id", "subject_id", "account", "body", "category", "context", "status", "created_at"},
	"gs feedback send":         {"id", "subject_id", "account", "body", "category", "context", "status", "created_at"},
	"gs feedback list":         {"feedback", "next_cursor"},
	"gs feedback resolve":      {"id", "status", "resolution", "resolved_by", "resolved_at"},
	"gs group list":            {"account", "groups"},
	"gs group delete":          {"account", "name", "handle", "deleted"},
	"gs context":               {"cwd", "config_path", "server_addr", "signed_in", "subject_id", "auth_reason", "workspace", "active_slice", "active_slice_source"},
	"gs config list":           {"config_path", "server_addr", "subject_id", "token_present"},
	"gs config get":            {"<key>"},
	"gs config set":            {"config_path", "server_addr", "subject_id", "token_present"},
	"gs alias list":            {"aliases"},
	"gs alias set":             {"name", "expansion"},
	"gs alias delete":          {"name", "deleted"},
	"gs rpc list":              {"methods"},
	"gs rpc call":              {"RPC response fields"},
	"gs check list":            {"changeset", "checks"},
	"gs check rerun":           {"name", "status", "run_id", "attempt"},
	"gs browse":                {"url"},
	"gs log":                   {"scope", "commits", "next_page_token"},
	"gs blame":                 {"commit_id", "path", "line_count", "truncated", "ranges", "commits"},
	"gs show":                  {"id", "short_id", "parent_ids", "root_tree_id", "author", "message", "created_at", "changed_paths"},
	"gs init":                  {"workspace", "slice_id", "base_commit_id", "client_object_cache", "hydrated"},
	"gs mount":                 {"id", "ref", "mount_path", "base_commit", "files"},
	"gs mount list":            {"workspaces"},
	"gs mount status":          {"id", "ref", "mount_path", "mounted", "read_only", "base_commit", "files", "local_edits", "daemon"},
	"gs mount discard":         {"id", "discarded"},
	"gs checkpoint":            {"number", "created_at", "message", "auto", "base_commit", "edits"},
	"gs checkpoint list":       {"checkpoints"},
	"gs rewind":                {"number", "created_at", "message", "auto", "base_commit", "edits"},
	"gs fork":                  {"number", "base_commit", "edits", "mount_path"},
	"gs cache":                 {"path", "objects", "bytes", "max_bytes", "pinned"},
	"gs cache status":          {"path", "objects", "bytes", "max_bytes", "pinned"},
	"gs cache prune":           {"objects", "bytes", "max_bytes", "removed", "removed_bytes", "pinned", "dry_run"},
	"gs restore":               {"restored"},
	"gs unmount":               {"id", "mount_path", "unmounted"},
	"gs fetch":                 {"files", "fetched", "fetched_bytes", "already_cached"},
	"gs workspace delete":      {"path", "kind", "slice", "deleted", "discarded_edits"},
	"gs workspace hydrate":     {"workspace", "base_commit_id", "client_object_cache", "hydrated"},
	"gs sync":                  {"workspace", "previous_base_commit_id", "new_base_commit_id", "status", "updated_paths", "local_paths", "merged_paths", "conflict_paths", "merge_strategy", "changeset_id", "patchset_id", "overlap_warnings"},
	"gs workspace sync":        {"workspace", "previous_base_commit_id", "new_base_commit_id", "status", "updated_paths", "local_paths", "merged_paths", "conflict_paths", "merge_strategy", "changeset_id", "patchset_id", "overlap_warnings"},
	"gs status":                {"workspace", "changed_path_count", "changed_paths", "conflict_count", "conflict_paths", "changeset_id", "patchset_id", "overlap_warnings"},
	"gs diff":                  {"workspace", "base_commit_id", "from_commit_id", "to_commit_id", "changed_path_count", "changed_paths", "diff"},
	"gs ci":                    {"workspace", "changed_path_count", "changed_paths", "results"},
	"gs create":                {"changeset_id", "patchset_id", "patchset_number", "dependency_id", "web_url", "submit", "overlap_warnings"},
	"gs modify":                {"changeset_id", "patchset_id", "patchset_number", "dependency_id", "web_url"},
	"gs submit":                {"dependency_id", "status", "results"},
	"gs deps":                  {"dependency_id", "status", "active_changeset_id", "root_changeset_id", "changesets"},
	"gs update-dependents":     {"dependency_id", "status", "updated_changesets"},
	"gs switch":                {"changeset_id", "patchset_id", "patchset_number", "dependency_id"},
	"gs up":                    {"changeset_id", "patchset_id", "patchset_number", "dependency_id"},
	"gs down":                  {"changeset_id", "patchset_id", "patchset_number", "dependency_id"},
	"gs top":                   {"changeset_id", "patchset_id", "patchset_number", "dependency_id"},
	"gs bottom":                {"changeset_id", "patchset_id", "patchset_number", "dependency_id"},
	"gs move":                  {"dependency_id", "status", "active_changeset_id", "root_changeset_id", "changesets"},
	"gs insert":                {"changeset_id", "patchset_id", "patchset_number", "dependency_id", "web_url"},
	"gs detach":                {"dependency_id", "status", "active_changeset_id", "root_changeset_id", "changesets"},
	"gs cs edit":               {"changeset", "title", "description"},
	"gs cs collaborators":      {"changeset", "changeset_id", "author", "original_author", "collaborators"},
	"gs cs collaborators list": {"changeset", "changeset_id", "author", "original_author", "collaborators"},
	"gs cs collaborators add":  {"changeset", "changeset_id", "collaborators"},
	"gs cs collaborators rm":   {"changeset", "changeset_id", "collaborators"},
	"gs cs handoff":            {"changeset", "changeset_id", "author", "original_author"},
	"gs cs list":               {"changesets"},
	"gs cs show":               {"changeset"},
	"gs cs status":             {"changeset", "checks"},
	"gs slice list":            {"account", "slices", "shared"},
	"gs slice paths":           {"ref", "included_paths"},
	"gs slice history":         {"ref", "slice_id", "versions"},
	"gs slice delete":          {"slice_id", "ref", "history_kept"},
	"gs slice rename":          {"id", "ref", "former_names", "previous_ref"},
	"gs slice archive":         {"id", "ref", "archived"},
	"gs slice unarchive":       {"id", "ref", "archived"},
	"gs intent":                {"id", "slice", "slice_id", "subject_id", "username", "controller_subject_id", "controller_username", "pattern", "message", "changeset_id", "expires_at", "created_at", "overlap_warnings"},
	"gs intent list":           {"slice", "intents"},
	"gs intent clear":          {"slice", "deleted_ids", "deleted"},
	"gs fs ls":                 {"path", "slice", "commit_id", "entries"},
	"gs fs cat":                {"path", "slice", "commit_id", "content_hash", "data_base64"},
	"gs fs mkdir":              {"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
	"gs fs write":              {"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
	"gs fs touch":              {"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
	"gs fs upload":             {"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
	"gs fs mv":                 {"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
	"gs fs rm":                 {"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
	"gs import":                {"source", "mount_path", "mode", "target_ref", "final_commit_id", "commits"},
	"gs version":               {"version", "commit", "build_date", "go_version", "dirty"},
}
