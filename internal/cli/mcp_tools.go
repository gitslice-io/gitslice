package cli

import (
	"bytes"
	"context"
	"path"
	"strings"
	"unicode/utf8"

	"google.golang.org/grpc"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/model"
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
	ref, err := corev1.NewRepositoryServiceClient(b.conn).GetRef(b.ctx(ctx), &corev1.GetRefRequest{RefName: model.DefaultTargetRef})
	if err != nil {
		return "", err
	}
	return ref.GetCommitId(), nil
}

func commitSummary(commit *corev1.Commit) map[string]any {
	if commit == nil {
		return map[string]any{}
	}
	out := map[string]any{"commit": commit.GetId(), "message": commit.GetMessage(), "author": commit.GetAuthor(), "created_at": commit.GetCreatedAt()}
	if commit.GetChangesetId() != "" {
		out["changeset"] = model.ShortChangesetID(commit.GetChangesetId())
	}
	return out
}

func isBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 || !utf8.Valid(data[:min(len(data), 8000)])
}

func badArgs(err error) error {
	return userError("invalid_arguments", "arguments: "+err.Error(), "")
}
