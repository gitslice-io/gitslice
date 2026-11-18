package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

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
}
