package mcpserver

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

// ReadTarget names a remote file to prefetch into the object cache.
type ReadTarget struct {
	Path        string
	ContentHash string
	Size        int64
}

// BackendDeps configures an MCP tool Backend.
type BackendDeps struct {
	Conn            *grpc.ClientConn
	Slices          SliceClient
	Repo            RepositoryClient
	Changesets      ChangesetClient
	Checks          CheckClient
	AuthCtx         func(ctx context.Context) context.Context
	Cache           *clientcache.ObjectCache
	DefaultSlice    string
	ResolveSliceRef func(ctx context.Context, value string) (*corev1.SliceRef, error)
	FetchSingle     func(ctx context.Context, slice *corev1.SliceRef, commitID, path string) ([]byte, bool, error)
	PrefetchToCache func(ctx context.Context, slice *corev1.SliceRef, commitID string, targets []ReadTarget) (map[string][]byte, error)
	ReadCached      func(hash string) ([]byte, bool, error)
	UploadEdits     func(ctx context.Context, slice *corev1.SliceRef, edits []*corev1.FileEdit, byHash map[string][]byte) error
	LatestCheckRuns func(runs []*corev1.CheckRun, patchsetID string) []*corev1.CheckRun
	WebResourceURL  func(path string) string
}

// Backend implements Gitslice's MCP tool suite against a gRPC connection and
// local object cache.
type Backend struct {
	deps BackendDeps
}

// NewBackend creates an MCP tool backend from deps.
func NewBackend(deps BackendDeps) *Backend {
	deps.DefaultSlice = strings.TrimSpace(deps.DefaultSlice)
	return &Backend{deps: deps}
}

func (b *Backend) ctx(ctx context.Context) context.Context {
	if b.deps.AuthCtx != nil {
		return b.deps.AuthCtx(ctx)
	}
	return ctx
}

// Schema builds a JSON Schema object definition for an MCP tool.
func Schema(required []string, properties map[string]any) map[string]any {
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// Str builds a JSON Schema string property definition.
func Str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

const sliceArg = "The slice, as account/slice. Defaults to gs mcp's --slice, or the workspace's."

// Tools returns the full set of Gitslice MCP tools backed by b.
func (b *Backend) Tools() []Tool {
	return []Tool{
		{
			Name:        "read_file",
			Description: "Read a file from a slice at its latest commit. Text comes back as text (up to 512 KiB, truncated beyond, with start_line/end_line to read a part); a binary file is reported, not returned.",
			InputSchema: Schema([]string{"path"}, map[string]any{
				"path":       Str("Repository path, such as /acme/app/main.go, or relative to the slice's folder."),
				"slice":      Str(sliceArg),
				"start_line": map[string]any{"type": "integer", "description": "First line to return, 1-based."},
				"end_line":   map[string]any{"type": "integer", "description": "Last line to return."},
			}),
			Call: b.readFile,
		},
		{
			Name:        "list_directory",
			Description: "List a folder in a slice: names, kinds and sizes. recursive lists everything under it (up to 2000 entries).",
			InputSchema: Schema(nil, map[string]any{
				"path":      Str("Folder path; defaults to the slice's folder."),
				"slice":     Str(sliceArg),
				"recursive": map[string]any{"type": "boolean", "description": "List everything under the folder."},
			}),
			Call: b.listDirectory,
		},
		{
			Name:        "search",
			Description: "Search file contents in a slice with a regular expression (RE2 syntax), like grep -n. Files are fetched once and kept in the local cache, so later searches are fast.",
			InputSchema: Schema([]string{"pattern"}, map[string]any{
				"pattern":     Str("Regular expression (RE2), matched per line."),
				"path":        Str("Folder to search; defaults to the slice's folder."),
				"slice":       Str(sliceArg),
				"glob":        Str("Only files whose path relative to the searched folder matches, such as **/*.go."),
				"ignore_case": map[string]any{"type": "boolean"},
				"max_results": map[string]any{"type": "integer", "description": "Matches to return, default 100, at most 1000."},
			}),
			Call: b.search,
		},
		{
			Name:        "history",
			Description: "Commits that changed a path (or the slice), newest first, with the changeset that landed each.",
			InputSchema: Schema(nil, map[string]any{
				"path":  Str("File or folder; defaults to the whole slice."),
				"slice": Str(sliceArg),
				"limit": map[string]any{"type": "integer", "description": "Default 20, at most 200."},
			}),
			Call: b.history,
		},
		{
			Name:        "blame",
			Description: "Who last changed each line of a file: runs of lines with their commit, author, time and changeset.",
			InputSchema: Schema([]string{"path"}, map[string]any{
				"path":  Str("File path."),
				"slice": Str(sliceArg),
			}),
			Call: b.blame,
		},
		{
			Name: "create_changeset",
			Description: "Make a changeset of edits on the latest commit, and submit it with submit: true. Each edit is " +
				`{"op":"write","path":...,"content":...}, {"op":"delete","path":...}, {"op":"move","from":...,"path":...}, ` +
				`{"op":"copy","from":...,"path":...} or {"op":"mkdir","path":...}. A submit that is refused leaves the ` +
				"changeset open and says why (status blocked, blocked_reason); a required check without a result yet is the usual reason, and submit_changeset tries again.",
			InputSchema: Schema([]string{"title", "edits"}, map[string]any{
				"title":       Str("Changeset title: one line, like a commit subject."),
				"description": Str("What and why, in Markdown. Shown on the changeset's page and, after the title, in the commit message."),
				"slice":       Str(sliceArg),
				"submit":      map[string]any{"type": "boolean", "description": "Submit after creating it."},
				"edits": map[string]any{
					"type": "array",
					"items": Schema([]string{"op", "path"}, map[string]any{
						"op":      map[string]any{"type": "string", "enum": []string{"write", "delete", "move", "copy", "mkdir"}},
						"path":    Str("The path written, deleted, made, or moved/copied to."),
						"from":    Str("For move and copy: the source path."),
						"content": Str("For write: the whole new file, as text."),
					}),
				},
			}),
			Call: b.createChangeset,
		},
		{
			Name:        "get_changeset",
			Description: "Where a changeset stands: status, why a submit was refused, its checks, and its conflicts.",
			InputSchema: Schema([]string{"changeset"}, map[string]any{
				"changeset": Str("Changeset id or handle, such as 8b4ee46085."),
			}),
			Call: b.getChangeset,
		},
		{
			Name:        "submit_changeset",
			Description: "Submit a changeset. If it is refused, the answer says why (status blocked, blocked_reason) and the changeset stays open.",
			InputSchema: Schema([]string{"changeset"}, map[string]any{
				"changeset": Str("Changeset id or handle."),
			}),
			Call: b.submitChangeset,
		},
	}
}

// Slice holds a resolved slice reference and its included repository folders.
type Slice struct {
	Ref      *corev1.SliceRef
	Included []string
}

func (b *Backend) slice(ctx context.Context, name string) (Slice, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = b.deps.DefaultSlice
	}
	if name == "" {
		return Slice{}, newUserError("slice_required", "no slice given", "Pass slice (account/slice) in the call, or start gs mcp with --slice.")
	}
	ref, err := b.deps.ResolveSliceRef(b.ctx(ctx), name)
	if err != nil {
		return Slice{}, err
	}
	resolved, err := b.slices().ResolveSlice(b.ctx(ctx), &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return Slice{}, err
	}
	return Slice{Ref: resolved.GetRef(), Included: resolved.GetDefinition().GetIncludedPaths()}, nil
}

// RepoPath makes p a repository path: absolute as given, or inside the slice's
// folder when the slice has one.
func (s Slice) RepoPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "/") {
		return path.Clean(p), nil
	}
	if len(s.Included) != 1 {
		if p == "" {
			return "", newUserError("path_required", "this slice has several folders; name one", "Use an absolute path such as "+strings.Join(s.Included, " or ")+".")
		}
		return "", newUserError("absolute_path_required", "use an absolute path in a slice with several folders", "Such as "+strings.Join(s.Included, " or ")+".")
	}
	return path.Clean(path.Join(s.Included[0], p)), nil
}

func (b *Backend) head(ctx context.Context) (string, error) {
	ref, err := b.repo().GetRef(b.ctx(ctx), &corev1.GetRefRequest{RefName: model.DefaultTargetRef})
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
	return newUserError("invalid_arguments", "arguments: "+err.Error(), "")
}
