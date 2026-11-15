package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	mcpReadMaxBytes    = 512 << 10
	mcpListMaxEntries  = 2000
	mcpSearchMaxFiles  = 10000
	mcpSearchFileBytes = 1 << 20
	mcpSearchBatch     = 200
	mcpSearchLineChars = 300
	mcpDefaultMatches  = 100
	mcpMaxMatches      = 1000
	mcpDefaultHistory  = 20
	mcpMaxHistory      = 200
	mcpMaxEditsPerCall = 500
)

// mcpBackend is what the tools work through: one connection, the signed-in
// user, the slice calls default to, and the local object cache.
type mcpBackend struct {
	r            Runner
	cfg          UserConfig
	conn         *grpc.ClientConn
	cache        *clientcache.ObjectCache
	defaultSlice string
}

func (r Runner) mcpTools(ctx context.Context, defaultSlice string) ([]mcpTool, func(), error) {
	cfg, conn, _, err := r.authenticatedConn(ctx)
	if err != nil {
		return nil, nil, err
	}
	cache, err := r.objectCache()
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if strings.TrimSpace(defaultSlice) == "" {
		if ws, err := r.readWorkspaceConfig(); err == nil && ws.Account != "" {
			defaultSlice = ws.Account + "/" + ws.Slice
		}
	}
	b := &mcpBackend{r: r, cfg: cfg, conn: conn, cache: cache, defaultSlice: strings.TrimSpace(defaultSlice)}
	return b.tools(), func() { conn.Close() }, nil
}

func (b *mcpBackend) ctx(ctx context.Context) context.Context { return authContext(ctx, b.cfg) }

func schema(required []string, properties map[string]any) map[string]any {
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

const sliceArg = "The slice, as account/slice. Defaults to gs mcp's --slice, or the workspace's."

func (b *mcpBackend) tools() []mcpTool {
	return []mcpTool{
		{
			Name:        "read_file",
			Description: "Read a file from a slice at its latest commit. Text comes back as text (up to 512 KiB, truncated beyond, with start_line/end_line to read a part); a binary file is reported, not returned.",
			InputSchema: schema([]string{"path"}, map[string]any{
				"path":       str("Repository path, such as /acme/app/main.go, or relative to the slice's folder."),
				"slice":      str(sliceArg),
				"start_line": map[string]any{"type": "integer", "description": "First line to return, 1-based."},
				"end_line":   map[string]any{"type": "integer", "description": "Last line to return."},
			}),
			call: b.readFile,
		},
		{
			Name:        "list_directory",
			Description: "List a folder in a slice: names, kinds and sizes. recursive lists everything under it (up to 2000 entries).",
			InputSchema: schema(nil, map[string]any{
				"path":      str("Folder path; defaults to the slice's folder."),
				"slice":     str(sliceArg),
				"recursive": map[string]any{"type": "boolean", "description": "List everything under the folder."},
			}),
			call: b.listDirectory,
		},
		{
			Name:        "search",
			Description: "Search file contents in a slice with a regular expression (RE2 syntax), like grep -n. Files are fetched once and kept in the local cache, so later searches are fast.",
			InputSchema: schema([]string{"pattern"}, map[string]any{
				"pattern":     str("Regular expression (RE2), matched per line."),
				"path":        str("Folder to search; defaults to the slice's folder."),
				"slice":       str(sliceArg),
				"glob":        str("Only files whose path relative to the searched folder matches, such as **/*.go."),
				"ignore_case": map[string]any{"type": "boolean"},
				"max_results": map[string]any{"type": "integer", "description": "Matches to return, default 100, at most 1000."},
			}),
			call: b.search,
		},
		{
			Name:        "history",
			Description: "Commits that changed a path (or the slice), newest first, with the changeset that landed each.",
			InputSchema: schema(nil, map[string]any{
				"path":  str("File or folder; defaults to the whole slice."),
				"slice": str(sliceArg),
				"limit": map[string]any{"type": "integer", "description": "Default 20, at most 200."},
			}),
			call: b.history,
		},
		{
			Name:        "blame",
			Description: "Who last changed each line of a file: runs of lines with their commit, author, time and changeset.",
			InputSchema: schema([]string{"path"}, map[string]any{
				"path":  str("File path."),
				"slice": str(sliceArg),
			}),
			call: b.blame,
		},
		{
			Name: "create_changeset",
			Description: "Make a changeset of edits on the latest commit, and submit it with submit: true. Each edit is " +
				`{"op":"write","path":...,"content":...}, {"op":"delete","path":...}, {"op":"move","from":...,"path":...}, ` +
				`{"op":"copy","from":...,"path":...} or {"op":"mkdir","path":...}. A submit that is refused leaves the ` +
				"changeset open and says why (status blocked, blocked_reason); a required check without a result yet is the usual reason, and submit_changeset tries again.",
			InputSchema: schema([]string{"title", "edits"}, map[string]any{
				"title":       str("Changeset title: one line, like a commit subject."),
				"description": str("What and why, in Markdown. Shown on the changeset's page and, after the title, in the commit message."),
				"slice":       str(sliceArg),
				"submit":      map[string]any{"type": "boolean", "description": "Submit after creating it."},
				"edits": map[string]any{
					"type": "array",
					"items": schema([]string{"op", "path"}, map[string]any{
						"op":      map[string]any{"type": "string", "enum": []string{"write", "delete", "move", "copy", "mkdir"}},
						"path":    str("The path written, deleted, made, or moved/copied to."),
						"from":    str("For move and copy: the source path."),
						"content": str("For write: the whole new file, as text."),
					}),
				},
			}),
			call: b.createChangeset,
		},
		{
			Name:        "get_changeset",
			Description: "Where a changeset stands: status, why a submit was refused, its checks, and its conflicts.",
			InputSchema: schema([]string{"changeset"}, map[string]any{
				"changeset": str("Changeset id or handle, such as 8b4ee46085."),
			}),
			call: b.getChangeset,
		},
		{
			Name:        "submit_changeset",
			Description: "Submit a changeset. If it is refused, the answer says why (status blocked, blocked_reason) and the changeset stays open.",
			InputSchema: schema([]string{"changeset"}, map[string]any{
				"changeset": str("Changeset id or handle."),
			}),
			call: b.submitChangeset,
		},
	}
}

// --- slices and paths

type mcpSlice struct {
	ref      *corev1.SliceRef
	included []string
}

func (b *mcpBackend) slice(ctx context.Context, name string) (mcpSlice, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = b.defaultSlice
	}
	if name == "" {
		return mcpSlice{}, userError("slice_required", "no slice given", "Pass slice (account/slice) in the call, or start gs mcp with --slice.")
	}
	ref, err := b.r.resolveSliceRefInput(b.ctx(ctx), b.cfg, b.conn, name)
	if err != nil {
		return mcpSlice{}, err
	}
	resolved, err := corev1.NewSliceServiceClient(b.conn).ResolveSlice(b.ctx(ctx), &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return mcpSlice{}, err
	}
	return mcpSlice{ref: resolved.GetRef(), included: resolved.GetDefinition().GetIncludedPaths()}, nil
}

// repoPath makes p a repository path: absolute as given, or inside the
// slice's folder when the slice has one.
func (s mcpSlice) repoPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "/") {
		return path.Clean(p), nil
	}
	if len(s.included) != 1 {
		if p == "" {
			return "", userError("path_required", "this slice has several folders; name one", "Use an absolute path such as "+strings.Join(s.included, " or ")+".")
		}
		return "", userError("absolute_path_required", "use an absolute path in a slice with several folders", "Such as "+strings.Join(s.included, " or ")+".")
	}
	return path.Clean(path.Join(s.included[0], p)), nil
}

func (b *mcpBackend) head(ctx context.Context) (string, error) {
	ref, err := corev1.NewRepositoryServiceClient(b.conn).GetRef(b.ctx(ctx), &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return "", err
	}
	return ref.GetCommitId(), nil
}

// --- tools

func (b *mcpBackend) readFile(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Path      string `json:"path"`
		Slice     string `json:"slice"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	s, err := b.slice(ctx, args.Slice)
	if err != nil {
		return nil, err
	}
	p, err := s.repoPath(args.Path)
	if err != nil {
		return nil, err
	}
	commit, err := b.head(ctx)
	if err != nil {
		return nil, err
	}
	file, err := corev1.NewRepositoryServiceClient(b.conn).ReadFile(b.ctx(ctx), &corev1.ReadFileRequest{CommitId: commit, Path: p, Slice: s.ref})
	if err != nil {
		return nil, err
	}
	data := file.GetData()
	out := map[string]any{"path": p, "size": len(data), "commit": commit}
	if isBinary(data) {
		out["binary"] = true
		return out, nil
	}
	text := string(data)
	if args.StartLine > 0 || args.EndLine > 0 {
		lines := strings.SplitAfter(text, "\n")
		start := max(args.StartLine, 1)
		end := len(lines)
		if args.EndLine > 0 && args.EndLine < end {
			end = args.EndLine
		}
		if start > end {
			text = ""
		} else {
			text = strings.Join(lines[start-1:end], "")
		}
		out["start_line"], out["end_line"] = start, end
	}
	if len(text) > mcpReadMaxBytes {
		text = text[:mcpReadMaxBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		out["truncated"] = true
	}
	out["content"] = text
	return out, nil
}

func (b *mcpBackend) listDirectory(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Path      string `json:"path"`
		Slice     string `json:"slice"`
		Recursive bool   `json:"recursive"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	s, err := b.slice(ctx, args.Slice)
	if err != nil {
		return nil, err
	}
	p, err := s.repoPath(args.Path)
	if err != nil {
		return nil, err
	}
	commit, err := b.head(ctx)
	if err != nil {
		return nil, err
	}
	entries, truncated, err := b.list(ctx, s, commit, p, args.Recursive, mcpListMaxEntries)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{"path": entry.GetPath(), "kind": entryKindName(entry.GetKind())}
		if entry.GetKind() == corev1.EntryKind_ENTRY_KIND_FILE {
			item["size"] = entry.GetSize()
		}
		out = append(out, item)
	}
	return map[string]any{"path": p, "entries": out, "truncated": truncated}, nil
}

// list lists a folder, recursively when asked (walking a level at a time if
// the server lists one level only), up to limit entries.
func (b *mcpBackend) list(ctx context.Context, s mcpSlice, commit, dir string, recursive bool, limit int) ([]*corev1.TreeEntry, bool, error) {
	client := corev1.NewRepositoryServiceClient(b.conn)
	var out []*corev1.TreeEntry
	pending := []string{dir}
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		cursor := ""
		for {
			page, err := client.ListDirectory(b.ctx(ctx), &corev1.ListDirectoryRequest{CommitId: commit, Path: current, Slice: s.ref, Cursor: cursor, PageSize: 1000, Recursive: recursive})
			if err != nil {
				return nil, false, err
			}
			for _, entry := range page.GetEntries() {
				if len(out) == limit {
					return out, true, nil
				}
				out = append(out, entry)
				if recursive && !page.GetRecursive() && entry.GetKind() == corev1.EntryKind_ENTRY_KIND_DIRECTORY {
					pending = append(pending, entry.GetPath())
				}
			}
			cursor = page.GetNextCursor()
			if cursor == "" {
				break
			}
		}
	}
	return out, false, nil
}

func (b *mcpBackend) search(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		Slice      string `json:"slice"`
		Glob       string `json:"glob"`
		IgnoreCase bool   `json:"ignore_case"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	pattern := args.Pattern
	if args.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, userError("invalid_pattern", "invalid regular expression: "+err.Error(), "Use RE2 syntax, as Go does.")
	}
	if args.Glob != "" && !doublestar.ValidatePattern(args.Glob) {
		return nil, userError("invalid_glob", "invalid glob "+args.Glob, "Use a pattern such as **/*.go.")
	}
	limit := args.MaxResults
	if limit <= 0 {
		limit = mcpDefaultMatches
	}
	limit = min(limit, mcpMaxMatches)
	s, err := b.slice(ctx, args.Slice)
	if err != nil {
		return nil, err
	}
	roots := s.included
	if strings.TrimSpace(args.Path) != "" || len(roots) == 1 {
		p, err := s.repoPath(args.Path)
		if err != nil {
			return nil, err
		}
		roots = []string{p}
	}
	commit, err := b.head(ctx)
	if err != nil {
		return nil, err
	}
	var files []*corev1.TreeEntry
	listTruncated := false
	for _, root := range roots {
		entries, truncated, err := b.list(ctx, s, commit, root, true, mcpSearchMaxFiles)
		if err != nil {
			return nil, err
		}
		listTruncated = listTruncated || truncated
		for _, entry := range entries {
			if entry.GetKind() != corev1.EntryKind_ENTRY_KIND_FILE || entry.GetSize() > mcpSearchFileBytes {
				continue
			}
			if args.Glob != "" {
				rel := strings.TrimPrefix(strings.TrimPrefix(entry.GetPath(), root), "/")
				if ok, _ := doublestar.Match(args.Glob, rel); !ok {
					continue
				}
			}
			files = append(files, entry)
		}
	}
	matches := []map[string]any{}
	searched := 0
	truncated := false
	for start := 0; start < len(files) && !truncated; start += mcpSearchBatch {
		batch := files[start:min(start+mcpSearchBatch, len(files))]
		contents, err := b.contents(ctx, s, commit, batch)
		if err != nil {
			return nil, err
		}
		for _, entry := range batch {
			data, ok := contents[entry.GetPath()]
			if !ok || isBinary(data) {
				continue
			}
			searched++
			for number, line := range strings.Split(string(data), "\n") {
				if !re.MatchString(line) {
					continue
				}
				if len(matches) == limit {
					truncated = true
					break
				}
				if len(line) > mcpSearchLineChars {
					line = line[:mcpSearchLineChars] + "…"
				}
				matches = append(matches, map[string]any{"path": entry.GetPath(), "line": number + 1, "text": strings.TrimRight(line, "\r")})
			}
			if truncated {
				break
			}
		}
	}
	return map[string]any{
		"matches":        matches,
		"files_searched": searched,
		"truncated":      truncated || listTruncated,
		"commit":         commit,
	}, nil
}

// contents reads files from the local cache, fetching the ones it lacks in
// one call and keeping them there.
func (b *mcpBackend) contents(ctx context.Context, s mcpSlice, commit string, entries []*corev1.TreeEntry) (map[string][]byte, error) {
	out := map[string][]byte{}
	var missing []string
	for _, entry := range entries {
		if hash := entry.GetContentHash(); hash != "" && b.cache.Exists(hash) {
			if data, err := b.cache.Read(hash); err == nil {
				out[entry.GetPath()] = data
				continue
			}
		}
		missing = append(missing, entry.GetPath())
	}
	if len(missing) == 0 {
		return out, nil
	}
	resp, err := corev1.NewRepositoryServiceClient(b.conn).ReadFiles(b.ctx(ctx), &corev1.ReadFilesRequest{CommitId: commit, Paths: missing, Slice: s.ref})
	if err != nil {
		return nil, err
	}
	for _, file := range resp.GetFiles() {
		out[file.GetPath()] = file.GetData()
		_, _ = b.cache.PutBytes(file.GetData())
	}
	return out, nil
}

func (b *mcpBackend) history(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Path  string `json:"path"`
		Slice string `json:"slice"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	s, err := b.slice(ctx, args.Slice)
	if err != nil {
		return nil, err
	}
	p := ""
	if strings.TrimSpace(args.Path) != "" {
		if p, err = s.repoPath(args.Path); err != nil {
			return nil, err
		}
	}
	limit := args.Limit
	if limit <= 0 {
		limit = mcpDefaultHistory
	}
	resp, err := corev1.NewRepositoryServiceClient(b.conn).ListCommits(b.ctx(ctx), &corev1.ListCommitsRequest{RefName: postgres.DefaultTargetRef, Slice: s.ref, Path: p, Limit: int32(min(limit, mcpMaxHistory))})
	if err != nil {
		return nil, err
	}
	commits := []map[string]any{}
	for _, commit := range resp.GetCommits() {
		commits = append(commits, commitSummary(commit))
	}
	return map[string]any{"path": p, "commits": commits, "more": resp.GetNextPageToken() != ""}, nil
}

func (b *mcpBackend) blame(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Path  string `json:"path"`
		Slice string `json:"slice"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	s, err := b.slice(ctx, args.Slice)
	if err != nil {
		return nil, err
	}
	p, err := s.repoPath(args.Path)
	if err != nil {
		return nil, err
	}
	resp, err := corev1.NewRepositoryServiceClient(b.conn).BlameFile(b.ctx(ctx), &corev1.BlameFileRequest{Path: p, Slice: s.ref})
	if err != nil {
		return nil, err
	}
	commits := map[string]*corev1.Commit{}
	for _, commit := range resp.GetCommits() {
		commits[commit.GetId()] = commit
	}
	ranges := []map[string]any{}
	for _, run := range resp.GetRanges() {
		item := map[string]any{"start_line": run.GetStartLine(), "line_count": run.GetLineCount()}
		for key, value := range commitSummary(commits[run.GetCommitId()]) {
			item[key] = value
		}
		item["commit"] = run.GetCommitId()
		ranges = append(ranges, item)
	}
	return map[string]any{"path": p, "commit": resp.GetCommitId(), "line_count": resp.GetLineCount(), "ranges": ranges, "truncated": resp.GetTruncated()}, nil
}

type mcpEdit struct {
	Op      string  `json:"op"`
	Path    string  `json:"path"`
	From    string  `json:"from"`
	Content *string `json:"content"`
}

func (b *mcpBackend) createChangeset(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Title       string    `json:"title"`
		Description string    `json:"description"`
		Slice       string    `json:"slice"`
		Submit      bool      `json:"submit"`
		Edits       []mcpEdit `json:"edits"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	if strings.TrimSpace(args.Title) == "" {
		return nil, userError("title_required", "a changeset needs a title", "Pass title, like a commit subject.")
	}
	if len(args.Edits) == 0 || len(args.Edits) > mcpMaxEditsPerCall {
		return nil, userError("invalid_edits", fmt.Sprintf("pass 1 to %d edits", mcpMaxEditsPerCall), "")
	}
	s, err := b.slice(ctx, args.Slice)
	if err != nil {
		return nil, err
	}
	commit, err := b.head(ctx)
	if err != nil {
		return nil, err
	}
	edits, err := b.fileEdits(ctx, s, args.Edits)
	if err != nil {
		return nil, err
	}
	client := corev1.NewChangesetServiceClient(b.conn)
	cs, err := client.CreateChangeset(b.ctx(ctx), &corev1.CreateChangesetRequest{
		AuthoringSlice: s.ref,
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   commit,
		Title:          strings.TrimSpace(args.Title),
		Description:    args.Description,
	})
	if err != nil {
		return nil, err
	}
	patchset, err := client.UpdateChangeset(b.ctx(ctx), &corev1.UpdateChangesetRequest{ChangesetId: cs.GetId(), BaseCommitId: commit, FileEdits: edits})
	if err != nil {
		_, _ = client.AbandonChangeset(b.ctx(ctx), &corev1.AbandonChangesetRequest{ChangesetId: cs.GetId()})
		return nil, err
	}
	out := changesetHeader(cs)
	out["patchset"] = patchset.GetNumber()
	out["changed_paths"] = patchset.GetChangedPaths()
	out["status"] = "draft"
	if args.Submit {
		for key, value := range b.submit(ctx, cs.GetId(), patchset.GetId()) {
			out[key] = value
		}
	}
	return out, nil
}

func (b *mcpBackend) fileEdits(ctx context.Context, s mcpSlice, edits []mcpEdit) ([]*corev1.FileEdit, error) {
	out := make([]*corev1.FileEdit, 0, len(edits))
	for i, edit := range edits {
		p, err := s.repoPath(edit.Path)
		if err != nil {
			return nil, err
		}
		switch edit.Op {
		case "write":
			if edit.Content == nil {
				return nil, userError("content_required", fmt.Sprintf("edit %d writes %s without content", i+1, p), "Pass the whole new file as content.")
			}
			uploaded, err := corev1.NewBlobServiceClient(b.conn).UploadBlob(b.ctx(ctx), &corev1.UploadBlobRequest{Data: []byte(*edit.Content), Slice: s.ref})
			if err != nil {
				return nil, err
			}
			out = append(out, &corev1.FileEdit{Op: "upsert", Path: p, BlobId: uploaded.GetBlobId(), ContentHash: uploaded.GetContentHash(), Mode: 0o100644})
		case "delete", "mkdir":
			out = append(out, &corev1.FileEdit{Op: edit.Op, Path: p})
		case "move", "copy":
			from, err := s.repoPath(edit.From)
			if err != nil || strings.TrimSpace(edit.From) == "" {
				return nil, userError("from_required", fmt.Sprintf("edit %d (%s) needs from, the source path", i+1, edit.Op), "")
			}
			op := "copy"
			if edit.Op == "move" {
				op = "rename"
			}
			out = append(out, &corev1.FileEdit{Op: op, OldPath: from, Path: p})
		default:
			return nil, userError("invalid_op", fmt.Sprintf("edit %d has op %q", i+1, edit.Op), "Use write, delete, move, copy or mkdir.")
		}
	}
	return out, nil
}

func (b *mcpBackend) getChangeset(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Changeset string `json:"changeset"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	cs, err := corev1.NewChangesetServiceClient(b.conn).GetChangeset(b.ctx(ctx), &corev1.GetChangesetRequest{ChangesetId: strings.TrimSpace(args.Changeset)})
	if err != nil {
		return nil, err
	}
	out := changesetHeader(cs)
	out["status"] = cs.GetStatus()
	if reason := cs.GetSubmitBlockedReason(); reason != "" && cs.GetStatus() != "submitted" {
		out["blocked_reason"] = reason
	}
	if cs.GetCommitId() != "" {
		out["commit_id"] = cs.GetCommitId()
	}
	if current := currentPatchset(cs); current != nil {
		out["patchset"] = current.GetNumber()
		if current.GetKind() != "" {
			out["patchset_kind"] = current.GetKind()
		}
		out["changed_paths"] = current.GetChangedPaths()
		var conflicts []string
		for _, conflict := range current.GetConflicts() {
			conflicts = append(conflicts, conflict.GetPath())
		}
		if len(conflicts) > 0 {
			out["conflicts"] = conflicts
		}
		runs, err := corev1.NewCheckServiceClient(b.conn).ListCheckRuns(b.ctx(ctx), &corev1.ListCheckRunsRequest{ChangesetId: cs.GetId(), PatchsetId: current.GetId()})
		if err == nil {
			checks := []map[string]any{}
			for _, run := range runs.GetRuns() {
				check := map[string]any{"name": run.GetCheckName(), "status": run.GetStatus()}
				if run.GetSummary() != "" {
					check["summary"] = run.GetSummary()
				}
				checks = append(checks, check)
			}
			out["checks"] = checks
		}
	}
	return out, nil
}

func (b *mcpBackend) submitChangeset(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Changeset string `json:"changeset"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, badArgs(err)
	}
	cs, err := corev1.NewChangesetServiceClient(b.conn).GetChangeset(b.ctx(ctx), &corev1.GetChangesetRequest{ChangesetId: strings.TrimSpace(args.Changeset)})
	if err != nil {
		return nil, err
	}
	out := changesetHeader(cs)
	for key, value := range b.submit(ctx, cs.GetId(), cs.GetCurrentPatchsetId()) {
		out[key] = value
	}
	return out, nil
}

// submit submits and says what became of it. A refusal is an answer, not an
// error: the changeset exists and its blocked_reason says what to do.
func (b *mcpBackend) submit(ctx context.Context, changesetID, patchsetID string) map[string]any {
	res, err := corev1.NewChangesetServiceClient(b.conn).SubmitChangeset(b.ctx(ctx), &corev1.SubmitChangesetRequest{ChangesetId: changesetID, ExpectedCurrentPatchsetId: patchsetID})
	if err != nil {
		out := map[string]any{"status": "blocked", "blocked_reason": grpcstatus.Convert(err).Message()}
		if check := pendingCheckPattern.FindStringSubmatch(grpcstatus.Convert(err).Message()); check != nil {
			out["waiting_for_check"] = check[1]
		}
		return out
	}
	out := map[string]any{"status": firstNonEmpty(res.GetStatus(), "submitted")}
	if res.GetCommitId() != "" {
		out["commit_id"] = res.GetCommitId()
	}
	return out
}

// --- shared shapes

func changesetHeader(cs *corev1.Changeset) map[string]any {
	handle := firstNonEmpty(storage.ShortChangesetID(cs.GetId()), cs.GetId())
	out := map[string]any{"changeset": handle, "changeset_id": cs.GetId(), "title": cs.GetTitle()}
	if cs.GetDescription() != "" {
		out["description"] = cs.GetDescription()
	}
	if url := webResourceURL("/cs/" + handle); url != "" {
		out["url"] = url
	}
	return out
}

func commitSummary(commit *corev1.Commit) map[string]any {
	if commit == nil {
		return map[string]any{}
	}
	out := map[string]any{"commit": commit.GetId(), "message": commit.GetMessage(), "author": commit.GetAuthor(), "created_at": commit.GetCreatedAt()}
	if commit.GetChangesetId() != "" {
		out["changeset"] = storage.ShortChangesetID(commit.GetChangesetId())
	}
	return out
}

func isBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 || !utf8.Valid(data[:min(len(data), 8000)])
}

func badArgs(err error) error {
	return userError("invalid_arguments", "arguments: "+err.Error(), "")
}

// mcpToolError is an error as a tool result: a stable code, the message, and
// what to do when the CLI knows.
func mcpToolError(err error) map[string]any {
	var cmdErr commandError
	if errors.As(err, &cmdErr) {
		out := map[string]any{"code": cmdErr.Code, "message": cmdErr.Message}
		if cmdErr.Hint != "" {
			out["hint"] = cmdErr.Hint
		}
		return out
	}
	if st, ok := grpcstatus.FromError(err); ok && st.Code() != codes.Unknown {
		return map[string]any{"code": strings.ToLower(st.Code().String()), "message": st.Message()}
	}
	return map[string]any{"code": "error", "message": err.Error()}
}
