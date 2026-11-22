package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (b *Backend) readFile(ctx context.Context, raw json.RawMessage) (any, error) {
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
	p, err := s.RepoPath(args.Path)
	if err != nil {
		return nil, err
	}
	commit, err := b.head(ctx)
	if err != nil {
		return nil, err
	}
	data, ok, err := b.deps.FetchSingle(b.ctx(ctx), s.Ref, commit, p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, grpcstatus.Errorf(codes.NotFound, "file not found: %s", p)
	}
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

func (b *Backend) listDirectory(ctx context.Context, raw json.RawMessage) (any, error) {
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
	p, err := s.RepoPath(args.Path)
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

func entryKindName(kind corev1.EntryKind) string {
	switch kind {
	case corev1.EntryKind_ENTRY_KIND_FILE:
		return "file"
	case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
		return "directory"
	case corev1.EntryKind_ENTRY_KIND_SYMLINK:
		return "symlink"
	default:
		return "unspecified"
	}
}

func (b *Backend) list(ctx context.Context, s Slice, commit, dir string, recursive bool, limit int) ([]*corev1.TreeEntry, bool, error) {
	client := b.repo()
	var out []*corev1.TreeEntry
	pending := []string{dir}
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		cursor := ""
		for {
			page, err := client.ListDirectory(b.ctx(ctx), &corev1.ListDirectoryRequest{CommitId: commit, Path: current, Slice: s.Ref, Cursor: cursor, PageSize: 1000, Recursive: recursive})
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

func (b *Backend) search(ctx context.Context, raw json.RawMessage) (any, error) {
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
		return nil, newUserError("invalid_pattern", "invalid regular expression: "+err.Error(), "Use RE2 syntax, as Go does.")
	}
	if args.Glob != "" && !doublestar.ValidatePattern(args.Glob) {
		return nil, newUserError("invalid_glob", "invalid glob "+args.Glob, "Use a pattern such as **/*.go.")
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
	roots := s.Included
	if strings.TrimSpace(args.Path) != "" || len(roots) == 1 {
		p, err := s.RepoPath(args.Path)
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
		contents, err := b.Contents(ctx, s, commit, batch)
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

func (b *Backend) Contents(ctx context.Context, s Slice, commit string, entries []*corev1.TreeEntry) (map[string][]byte, error) {
	out := make(map[string][]byte, len(entries))
	var missing []ReadTarget
	var missingEntries []*corev1.TreeEntry
	for _, entry := range entries {
		hash := entry.GetContentHash()
		if hash != "" && b.deps.Cache != nil && b.deps.Cache.Exists(hash) && b.deps.ReadCached != nil {
			data, ok, err := b.deps.ReadCached(hash)
			if err != nil {
				return nil, err
			}
			if ok {
				out[entry.GetPath()] = data
				continue
			}
		}
		missing = append(missing, ReadTarget{
			Path:        entry.GetPath(),
			ContentHash: hash,
			Size:        entry.GetSize(),
		})
		missingEntries = append(missingEntries, entry)
	}
	if len(missing) == 0 {
		return out, nil
	}
	unhashed, err := b.deps.PrefetchToCache(b.ctx(ctx), s.Ref, commit, missing)
	if err != nil {
		return nil, err
	}
	for _, entry := range missingEntries {
		if data, ok := unhashed[entry.GetPath()]; ok {
			out[entry.GetPath()] = data
			continue
		}
		hash := entry.GetContentHash()
		if hash == "" || b.deps.ReadCached == nil {
			continue
		}
		data, ok, err := b.deps.ReadCached(hash)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("missing fetched content for %s (%s)", entry.GetPath(), hash)
		}
		out[entry.GetPath()] = data
	}
	return out, nil
}

func (b *Backend) history(ctx context.Context, raw json.RawMessage) (any, error) {
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
		if p, err = s.RepoPath(args.Path); err != nil {
			return nil, err
		}
	}
	limit := args.Limit
	if limit <= 0 {
		limit = mcpDefaultHistory
	}
	resp, err := b.repo().ListCommits(b.ctx(ctx), &corev1.ListCommitsRequest{RefName: model.DefaultTargetRef, Slice: s.Ref, Path: p, Limit: int32(min(limit, mcpMaxHistory))})
	if err != nil {
		return nil, err
	}
	commits := []map[string]any{}
	for _, commit := range resp.GetCommits() {
		commits = append(commits, commitSummary(commit))
	}
	return map[string]any{"path": p, "commits": commits, "more": resp.GetNextPageToken() != ""}, nil
}

func (b *Backend) blame(ctx context.Context, raw json.RawMessage) (any, error) {
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
	p, err := s.RepoPath(args.Path)
	if err != nil {
		return nil, err
	}
	resp, err := b.repo().BlameFile(b.ctx(ctx), &corev1.BlameFileRequest{Path: p, Slice: s.Ref})
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
