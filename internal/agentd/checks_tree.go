package agentd

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func lookupCheckTreeEntry(ctx context.Context, repo RepoClient, rootTreeID, prefix string) (*corev1.TreeEntry, error) {
	prefix, err := NormalizeCheckMaterializePath(prefix)
	if err != nil {
		return nil, err
	}
	parent := "/"
	base := prefix
	if slash := strings.LastIndex(prefix, "/"); slash >= 0 {
		parent = prefix[:slash]
		if parent == "" {
			parent = "/"
		}
		base = prefix[slash+1:]
	}
	entries, err := listCheckDirectoryAll(ctx, repo, &corev1.ListDirectoryRequest{
		RootTreeId: rootTreeID,
		Path:       logicalAbsPath(parent),
		PageSize:   CheckMaterializePageSize,
	})
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		entryPath, err := entryLogicalPath(entry)
		if err == nil && entryPath == prefix {
			return entry, nil
		}
		if entry.GetName() == base {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("check materialize path not found: %s", prefix)
}

func listCheckDirectoryAll(ctx context.Context, repo RepoClient, req *corev1.ListDirectoryRequest) ([]*corev1.TreeEntry, error) {
	if req == nil {
		req = &corev1.ListDirectoryRequest{}
	}
	pageReq := &corev1.ListDirectoryRequest{
		CommitId:   req.GetCommitId(),
		Path:       req.GetPath(),
		Cursor:     req.GetCursor(),
		PageSize:   req.GetPageSize(),
		Slice:      req.GetSlice(),
		RootTreeId: req.GetRootTreeId(),
	}
	var entries []*corev1.TreeEntry
	for {
		page, err := repo.ListDirectory(ctx, pageReq)
		if err != nil {
			return nil, err
		}
		entries = append(entries, page.GetEntries()...)
		if strings.TrimSpace(page.GetNextCursor()) == "" {
			return entries, nil
		}
		if page.GetNextCursor() == pageReq.Cursor {
			return nil, fmt.Errorf("directory pagination did not advance for %s", pageReq.Path)
		}
		pageReq.Cursor = page.GetNextCursor()
	}
}

func NormalizeCheckMaterializePrefixes(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		p, err := NormalizeCheckMaterializePath(value)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		cleaned = append(cleaned, p)
	}
	sort.Slice(cleaned, func(i, j int) bool {
		leftDepth := checkPathDepth(cleaned[i])
		rightDepth := checkPathDepth(cleaned[j])
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return cleaned[i] < cleaned[j]
	})
	out := make([]string, 0, len(cleaned))
	for _, candidate := range cleaned {
		nested := false
		for _, existing := range out {
			if checkPathContains(existing, candidate) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, candidate)
		}
	}
	return out, nil
}

func ensureNotReservedGsPath(p string) error {
	normalized := strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".gs" {
			return NewError("reserved_path", "path refers to reserved .gs metadata: "+p, "Choose a path outside .gs.")
		}
	}
	return nil
}

func NormalizeCheckMaterializePath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || value == "." || value == "/" {
		return "/", nil
	}
	trimmed := strings.TrimPrefix(value, "/")
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", fmt.Errorf("check materialize path %q contains .. segment", value)
		}
		if segment == ".gs" {
			return "", ensureNotReservedGsPath(value)
		}
	}
	cleaned := path.Clean("/" + trimmed)
	if cleaned == "/" {
		return "/", nil
	}
	if err := ensureNotReservedGsPath(cleaned); err != nil {
		return "", err
	}
	return strings.TrimPrefix(cleaned, "/"), nil
}

func entryLogicalPath(entry *corev1.TreeEntry) (string, error) {
	if entry == nil {
		return "", fmt.Errorf("tree entry is nil")
	}
	p := strings.TrimSpace(entry.GetPath())
	if p == "" {
		return "", fmt.Errorf("tree entry path is empty")
	}
	return NormalizeCheckMaterializePath(p)
}

func checkMaterializeParentDir(filePath string) string {
	if filePath == "/" || !strings.Contains(filePath, "/") {
		return "/"
	}
	return filePath[:strings.LastIndex(filePath, "/")]
}

func checkPathDepth(p string) int {
	if p == "/" {
		return 0
	}
	return strings.Count(p, "/") + 1
}

func checkPathContains(prefix, p string) bool {
	if prefix == "/" {
		return true
	}
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func logicalAbsPath(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return "/" + path.Clean(p)
}

func CheckTreeTarget(destRoot, logicalPath string) (string, error) {
	target, err := CheckTreeLocalPath(destRoot, logicalPath)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(destRoot)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." {
		return "", fmt.Errorf("check materialize file target must be inside destination: %s", logicalPath)
	}
	if err := ensureNotReservedGsPath(filepath.ToSlash(rel)); err != nil {
		return "", err
	}
	return target, nil
}

func CheckTreeLocalPath(destRoot, logicalPath string) (string, error) {
	cleaned, err := NormalizeCheckMaterializePath(logicalPath)
	if err != nil {
		return "", err
	}
	rel := "."
	if cleaned != "/" {
		if err := ensureNotReservedGsPath(cleaned); err != nil {
			return "", err
		}
		rel = filepath.FromSlash(cleaned)
	}
	root, err := filepath.Abs(destRoot)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, rel)
	if err := ensureCheckLocalPathInside(root, target); err != nil {
		return "", err
	}
	return target, nil
}

func ensureCheckLocalPathInside(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("resolve check materialize path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("check materialize target escapes destination: %s", target)
	}
	return nil
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
		return kind.String()
	}
}

type CheckTreeRepoClient struct {
	Inner   RepoClient
	Account string
}

func NewCheckTreeRepoClient(inner RepoClient, account string) CheckTreeRepoClient {
	return CheckTreeRepoClient{Inner: inner, Account: account}
}

func (c CheckTreeRepoClient) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest, opts ...grpc.CallOption) (*corev1.ListDirectoryResponse, error) {
	mapped := &corev1.ListDirectoryRequest{
		CommitId:   req.GetCommitId(),
		Path:       c.StoragePath(req.GetPath()),
		Cursor:     req.GetCursor(),
		PageSize:   req.GetPageSize(),
		Slice:      req.GetSlice(),
		RootTreeId: req.GetRootTreeId(),
	}
	res, err := c.Inner.ListDirectory(ctx, mapped, opts...)
	if err != nil {
		return nil, err
	}
	out := &corev1.ListDirectoryResponse{NextCursor: res.GetNextCursor()}
	for _, entry := range res.GetEntries() {
		if entry == nil {
			continue
		}
		out.Entries = append(out.Entries, &corev1.TreeEntry{
			Path:          c.LogicalPath(entry.GetPath()),
			Name:          entry.GetName(),
			Kind:          entry.GetKind(),
			Mode:          entry.GetMode(),
			TreeId:        entry.GetTreeId(),
			BlobId:        entry.GetBlobId(),
			SymlinkTarget: entry.GetSymlinkTarget(),
			Size:          entry.GetSize(),
			ContentHash:   entry.GetContentHash(),
		})
	}
	return out, nil
}

func (c CheckTreeRepoClient) ReadFiles(ctx context.Context, req *corev1.ReadFilesRequest, opts ...grpc.CallOption) (*corev1.ReadFilesResponse, error) {
	inner, ok := c.Inner.(BatchReadClient)
	if !ok {
		return nil, grpcstatus.Error(codes.Unimplemented, "repository client cannot read files in batches")
	}
	paths := make([]string, len(req.GetPaths()))
	for i, p := range req.GetPaths() {
		paths[i] = c.StoragePath(p)
	}
	return inner.ReadFiles(ctx, &corev1.ReadFilesRequest{
		CommitId:   req.GetCommitId(),
		Paths:      paths,
		RootTreeId: req.GetRootTreeId(),
		Slice:      req.GetSlice(),
	}, opts...)
}

func (c CheckTreeRepoClient) ReadFile(ctx context.Context, req *corev1.ReadFileRequest, opts ...grpc.CallOption) (*corev1.ReadFileResponse, error) {
	mapped := &corev1.ReadFileRequest{
		CommitId:   req.GetCommitId(),
		Path:       c.StoragePath(req.GetPath()),
		Offset:     req.GetOffset(),
		Length:     req.GetLength(),
		RootTreeId: req.GetRootTreeId(),
		Slice:      req.GetSlice(),
	}
	return c.Inner.ReadFile(ctx, mapped, opts...)
}

func (c CheckTreeRepoClient) StoragePath(logicalPath string) string {
	account := strings.Trim(strings.TrimSpace(c.Account), "/")
	if account == "" {
		return logicalAbsPath(logicalPath)
	}
	cleaned := logicalAbsPath(logicalPath)
	if cleaned == "/" {
		return "/" + account
	}
	return "/" + path.Join(account, strings.TrimPrefix(cleaned, "/"))
}

func (c CheckTreeRepoClient) LogicalPath(storagePath string) string {
	account := strings.Trim(strings.TrimSpace(c.Account), "/")
	cleaned := logicalAbsPath(storagePath)
	if account == "" {
		return cleaned
	}
	accountRoot := "/" + account
	switch {
	case cleaned == accountRoot:
		return "/"
	case strings.HasPrefix(cleaned, accountRoot+"/"):
		return "/" + strings.TrimPrefix(cleaned, accountRoot+"/")
	default:
		return cleaned
	}
}
