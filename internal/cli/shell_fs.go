package cli

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func (s *serverShell) cd(ctx context.Context, target string) error {
	globalPath, err := s.resolve(target)
	if err != nil {
		return err
	}
	if globalPath == s.root {
		s.cwd = globalPath
		return nil
	}
	entry, err := s.resolveEntry(ctx, globalPath)
	if err != nil {
		return s.lookupError(err, globalPath)
	}
	if entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
		return fmt.Errorf("%s is not a directory", s.shellPath(globalPath))
	}
	s.cwd = globalPath
	return nil
}

func (s *serverShell) ls(ctx context.Context, target string) error {
	globalPath, err := s.resolve(target)
	if err != nil {
		return err
	}
	entry, err := s.resolveEntry(ctx, globalPath)
	if err == nil && entry.Kind == corev1.EntryKind_ENTRY_KIND_FILE {
		fmt.Fprintln(s.stdout, s.entryName(entry))
		return nil
	}
	if err != nil && (grpcstatus.Code(err) != codes.NotFound || globalPath != s.root) {
		return s.lookupError(err, globalPath)
	}
	entries, err := s.directoryEntries(ctx, globalPath)
	if err != nil {
		return s.lookupError(err, globalPath)
	}
	for _, entry := range entries {
		name := s.entryName(entry)
		if entry.Kind == corev1.EntryKind_ENTRY_KIND_DIRECTORY {
			name = s.colorize(ansiBlue, name+"/")
		}
		fmt.Fprintln(s.stdout, name)
	}
	return nil
}

func (s *serverShell) directoryEntries(ctx context.Context, globalPath string) ([]*corev1.TreeEntry, error) {
	var entries []*corev1.TreeEntry
	listEntries, err := listDirectoryAll(ctx, s.repo, &corev1.ListDirectoryRequest{CommitId: s.commitID, Path: globalPath, PageSize: 1000})
	if err != nil {
		if grpcstatus.Code(err) != codes.NotFound {
			return nil, err
		}
		if globalPath != s.root && s.projectionDirectoryEntry(globalPath) == nil {
			return nil, err
		}
	} else {
		entries = append([]*corev1.TreeEntry(nil), listEntries...)
	}
	entries = s.projectDirectoryEntries(globalPath, entries)
	entries = s.withSyntheticDirectoryEntries(globalPath, entries)
	sort.Slice(entries, func(i, j int) bool {
		return s.entryName(entries[i]) < s.entryName(entries[j])
	})
	return entries, nil
}

func (s *serverShell) cat(ctx context.Context, target string) error {
	globalPath, err := s.resolve(target)
	if err != nil {
		return err
	}
	entry, err := s.resolveEntry(ctx, globalPath)
	if err != nil {
		return s.lookupError(err, globalPath)
	}
	if entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE {
		return fmt.Errorf("%s is not a file", s.shellPath(globalPath))
	}
	read, err := s.repo.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: s.commitID, Path: globalPath})
	if err != nil {
		return err
	}
	if _, err := s.stdout.Write(read.Data); err != nil {
		return err
	}
	if len(read.Data) == 0 || read.Data[len(read.Data)-1] != '\n' {
		fmt.Fprintln(s.stdout)
	}
	return nil
}

func (s *serverShell) write(ctx context.Context, target string, data []byte) error {
	p, err := s.resolve(target)
	if err != nil {
		return err
	}
	if s.mutator == nil {
		return fmt.Errorf("mutations are unavailable in this shell scope")
	}
	cleaned, err := normalizeMutationPath(s.mutator.slice, p)
	if err != nil {
		return err
	}
	edit, err := s.runner.uploadFileEdit(authContext(ctx, s.mutator.cfg), s.mutator.conn, s.mutator.slice.Ref, cleaned, data)
	if err != nil {
		return err
	}
	return s.mutate(ctx, "write", []*corev1.FileEdit{edit})
}

func (s *serverShell) mutate(ctx context.Context, operation string, edits []*corev1.FileEdit) error {
	if s.mutator == nil {
		return fmt.Errorf("mutations are unavailable in this shell scope")
	}
	var captured strings.Builder
	originalStdout := s.mutator.runner.Stdout
	s.mutator.runner.Stdout = &captured
	err := s.mutator.apply(ctx, commandOptions{Quiet: true}, operation, edits)
	s.mutator.runner.Stdout = originalStdout
	if err != nil {
		return err
	}
	ref, err := s.repo.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return err
	}
	s.commitID = ref.CommitId
	// Name the changeset, not the commit: its link is the one to share.
	landed := s.mutator.last
	fmt.Fprintf(s.stdout, "%s %s through changeset %s", s.colorize(ansiGreen, "ok"), operationPastTense(operation), landed.Changeset)
	if landed.ChangesetURL != "" {
		fmt.Fprintf(s.stdout, " %s", landed.ChangesetURL)
	}
	fmt.Fprintln(s.stdout)
	return nil
}

func (s *serverShell) stat(ctx context.Context, target string) error {
	globalPath, err := s.resolve(target)
	if err != nil {
		return err
	}
	if globalPath == s.root {
		if _, err := s.resolveEntry(ctx, globalPath); err != nil && grpcstatus.Code(err) != codes.NotFound {
			return err
		}
		fmt.Fprintf(s.stdout, "path: %s\n", globalPath)
		fmt.Fprintf(s.stdout, "shell_path: %s\n", s.shellPath(globalPath))
		fmt.Fprintln(s.stdout, "kind: directory")
		return nil
	}
	entry, err := s.resolveEntry(ctx, globalPath)
	if err != nil {
		return s.lookupError(err, globalPath)
	}
	fmt.Fprintf(s.stdout, "path: %s\n", entry.Path)
	fmt.Fprintf(s.stdout, "shell_path: %s\n", s.shellPath(entry.Path))
	fmt.Fprintf(s.stdout, "kind: %s\n", entryKindName(entry.Kind))
	if entry.Mode != 0 {
		fmt.Fprintf(s.stdout, "mode: %o\n", entry.Mode)
	}
	if entry.Size != 0 {
		fmt.Fprintf(s.stdout, "size: %d\n", entry.Size)
	}
	if entry.ContentHash != "" {
		fmt.Fprintf(s.stdout, "content_hash: %s\n", entry.ContentHash)
	}
	if entry.TreeId != "" {
		fmt.Fprintf(s.stdout, "tree_id: %s\n", entry.TreeId)
	}
	if entry.BlobId != "" {
		fmt.Fprintf(s.stdout, "blob_id: %s\n", entry.BlobId)
	}
	return nil
}

func (s *serverShell) lookupError(err error, globalPath string) error {
	if grpcstatus.Code(err) == codes.NotFound {
		return fmt.Errorf("path not found: %s", s.shellPath(globalPath))
	}
	return err
}

func (s *serverShell) resolveEntry(ctx context.Context, globalPath string) (*corev1.TreeEntry, error) {
	resolved, err := s.repo.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: s.commitID, Path: globalPath})
	if err != nil {
		if grpcstatus.Code(err) == codes.NotFound {
			if entry, ok := s.syntheticDirs[globalPath]; ok {
				return entry, nil
			}
			if entry := s.projectionDirectoryEntry(globalPath); entry != nil {
				return entry, nil
			}
		}
		return nil, err
	}
	if resolved.Entry == nil {
		return nil, fmt.Errorf("path not found: %s", s.shellPath(globalPath))
	}
	return resolved.Entry, nil
}

func (s *serverShell) withSyntheticDirectoryEntries(globalPath string, entries []*corev1.TreeEntry) []*corev1.TreeEntry {
	if len(s.syntheticDirs) == 0 {
		return entries
	}
	byPath := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry != nil {
			byPath[entry.Path] = struct{}{}
		}
	}
	for p, entry := range s.syntheticDirs {
		if entry == nil || path.Dir(p) != globalPath {
			continue
		}
		if _, ok := byPath[p]; ok {
			continue
		}
		entries = append(entries, entry)
		byPath[p] = struct{}{}
	}
	return entries
}

func (s *serverShell) projectDirectoryEntries(globalPath string, entries []*corev1.TreeEntry) []*corev1.TreeEntry {
	if len(s.projection) == 0 {
		return entries
	}
	byPath := make(map[string]struct{}, len(entries)+len(s.projection))
	out := make([]*corev1.TreeEntry, 0, len(entries)+len(s.projection))
	for _, entry := range entries {
		if entry == nil || !s.pathInProjection(entry.Path) {
			continue
		}
		out = append(out, entry)
		byPath[entry.Path] = struct{}{}
	}
	for _, root := range s.projection {
		childPath := projectionChildPath(globalPath, root)
		if childPath == "" {
			continue
		}
		if _, ok := byPath[childPath]; ok {
			continue
		}
		out = append(out, &corev1.TreeEntry{
			Path: childPath,
			Name: path.Base(childPath),
			Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY,
		})
		byPath[childPath] = struct{}{}
	}
	return out
}

func projectionChildPath(parent, includedRoot string) string {
	parent = strings.TrimRight(parent, "/")
	if parent == "" {
		parent = "/"
	}
	if parent == includedRoot {
		return ""
	}
	if !paths.Contains(parent, includedRoot) {
		return ""
	}
	rel := strings.TrimPrefix(includedRoot, strings.TrimRight(parent, "/")+"/")
	if parent == "/" {
		rel = strings.TrimPrefix(includedRoot, "/")
	}
	if rel == "" {
		return ""
	}
	child := strings.Split(rel, "/")[0]
	if parent == "/" {
		return "/" + child
	}
	return strings.TrimRight(parent, "/") + "/" + child
}

func (s *serverShell) projectionDirectoryEntry(globalPath string) *corev1.TreeEntry {
	if len(s.projection) == 0 {
		return nil
	}
	if globalPath == s.root {
		return &corev1.TreeEntry{Path: globalPath, Name: path.Base(globalPath), Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY}
	}
	for _, root := range s.projection {
		if paths.Contains(globalPath, root) {
			return &corev1.TreeEntry{Path: globalPath, Name: path.Base(globalPath), Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY}
		}
	}
	return nil
}

func (s *serverShell) pathInProjection(globalPath string) bool {
	if len(s.projection) == 0 {
		return true
	}
	if globalPath == s.root {
		return true
	}
	for _, root := range s.projection {
		if paths.Contains(root, globalPath) || paths.Contains(globalPath, root) {
			return true
		}
	}
	return false
}

func (s *serverShell) resolve(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return s.cwd, nil
	}
	var candidate string
	if !s.scoped {
		if strings.HasPrefix(value, "/") {
			candidate = value
		} else {
			candidate = strings.TrimRight(s.cwd, "/") + "/" + value
		}
	} else {
		scopeRoot := "/" + s.scope
		switch {
		case strings.HasPrefix(value, "/") && (value == s.root || strings.HasPrefix(value, strings.TrimRight(s.root, "/")+"/")):
			candidate = value
		case value == scopeRoot || strings.HasPrefix(value, scopeRoot+"/"):
			candidate = value
		case strings.HasPrefix(value, "/"):
			segments := strings.Split(strings.Trim(value, "/"), "/")
			scopeAccount, _, _ := storage.SplitSliceHandle(s.scope)
			if len(segments) >= 2 && scopeAccount != "" && segments[0] == scopeAccount {
				candidate = value
			} else {
				candidate = strings.TrimRight(s.root, "/") + "/" + strings.TrimPrefix(value, "/")
			}
		default:
			candidate = strings.TrimRight(s.cwd, "/") + "/" + value
		}
	}
	cleaned, err := cleanShellGlobalPath(candidate)
	if err != nil {
		return "", err
	}
	if !paths.Contains(s.root, cleaned) {
		return "", userError("outside_slice", "path is outside the workspace slice: "+cleaned, "Use paths under "+s.shellPath(s.root)+".")
	}
	if !s.pathInProjection(cleaned) {
		scopeKind := "workspace slice"
		if s.root == "/" {
			scopeKind = "attached slice"
		}
		return "", userError("outside_slice", "path is outside the "+scopeKind+": "+cleaned, "Use paths included by "+s.scope+".")
	}
	return cleaned, nil
}
