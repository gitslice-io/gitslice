package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type fsListOutput struct {
	Path     string          `json:"path"`
	Slice    string          `json:"slice"`
	CommitID string          `json:"commit_id"`
	Entries  []fsEntryOutput `json:"entries"`
}

type fsEntryOutput struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Mode        uint32 `json:"mode,omitempty"`
	Size        int64  `json:"size,omitempty"`
	TreeID      string `json:"tree_id,omitempty"`
	BlobID      string `json:"blob_id,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
}

type fsCatOutput struct {
	Path        string `json:"path"`
	Slice       string `json:"slice"`
	CommitID    string `json:"commit_id"`
	ContentHash string `json:"content_hash,omitempty"`
	DataBase64  string `json:"data_base64"`
}

func (r Runner) runFSList(ctx context.Context, opts commandOptions, requestedPath string) error {
	cfg, conn, slice, repo, commitID, err := r.homeFSReadScope(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	root, err := homeSliceRoot(slice)
	if err != nil {
		return err
	}
	p := strings.TrimSpace(requestedPath)
	usingDefaultPath := p == ""
	if p == "" {
		p = root
	}
	p, err = normalizeHomePath(slice, p, true)
	if err != nil {
		return err
	}
	color := r.colorEnabled(opts)
	if usingDefaultPath && !opts.jsonOutput() && !opts.Quiet {
		scope := slice.Ref.Account + ":" + slice.Ref.Slice
		fmt.Fprintf(r.stderr(), "%s listing %s at %s\n", colorize(color, ansiDim, "remote:"), scope, p)
	}
	callCtx := authContext(ctx, cfg)
	var entries []*corev1.TreeEntry
	resolved, err := repo.ResolvePath(callCtx, &corev1.ResolvePathRequest{CommitId: commitID, Path: p})
	if err != nil {
		if grpcstatus.Code(err) != codes.NotFound || p != root {
			return err
		}
		entries = []*corev1.TreeEntry{}
	} else if resolved.Entry != nil && resolved.Entry.Kind == corev1.EntryKind_ENTRY_KIND_FILE {
		entries = []*corev1.TreeEntry{resolved.Entry}
	} else {
		listEntries, err := listDirectoryAll(callCtx, repo, &corev1.ListDirectoryRequest{CommitId: commitID, Path: p, PageSize: 1000})
		if err != nil {
			return err
		}
		entries = append(entries, listEntries...)
	}
	sort.Slice(entries, func(i, j int) bool {
		return fsEntryName(entries[i]) < fsEntryName(entries[j])
	})
	if opts.jsonOutput() {
		out := fsListOutput{
			Path:     p,
			Slice:    slice.Ref.Account + ":" + slice.Ref.Slice,
			CommitID: commitID,
			Entries:  make([]fsEntryOutput, 0, len(entries)),
		}
		for _, entry := range entries {
			out.Entries = append(out.Entries, fsEntryOutputFromProto(entry))
		}
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	for _, entry := range entries {
		name := fsEntryName(entry)
		if entry.Kind == corev1.EntryKind_ENTRY_KIND_DIRECTORY {
			name += "/"
			name = colorize(color, ansiBlue, name)
		}
		fmt.Fprintln(r.Stdout, name)
	}
	return nil
}

func (r Runner) runFSCat(ctx context.Context, opts commandOptions, requestedPath string) error {
	cfg, conn, slice, repo, commitID, err := r.homeFSReadScope(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	p, err := normalizeHomePath(slice, requestedPath, true)
	if err != nil {
		return err
	}
	callCtx := authContext(ctx, cfg)
	resolved, err := repo.ResolvePath(callCtx, &corev1.ResolvePathRequest{CommitId: commitID, Path: p})
	if err != nil {
		return err
	}
	if resolved.Entry == nil || resolved.Entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE {
		return fmt.Errorf("%s is not a file", p)
	}
	if resolved.Entry.Size > blobStreamThresholdBytes && resolved.Entry.ContentHash != "" {
		blobClient := corev1.NewBlobServiceClient(conn)
		if opts.jsonOutput() {
			data, err := readBlobStreamBytes(callCtx, blobClient, slice.Ref, resolved.Entry.ContentHash, 0, 0)
			if err != nil {
				return err
			}
			return r.writeJSONOutput(opts, fsCatOutput{
				Path:        p,
				Slice:       slice.Ref.Account + ":" + slice.Ref.Slice,
				CommitID:    commitID,
				ContentHash: resolved.Entry.ContentHash,
				DataBase64:  base64.StdEncoding.EncodeToString(data),
			})
		}
		if opts.Quiet {
			return nil
		}
		return writeBlobStream(callCtx, blobClient, slice.Ref, resolved.Entry.ContentHash, 0, 0, r.stdout())
	}
	read, err := repo.ReadFile(callCtx, &corev1.ReadFileRequest{CommitId: commitID, Path: p})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, fsCatOutput{
			Path:        p,
			Slice:       slice.Ref.Account + ":" + slice.Ref.Slice,
			CommitID:    commitID,
			ContentHash: read.ContentHash,
			DataBase64:  base64.StdEncoding.EncodeToString(read.Data),
		})
	}
	if opts.Quiet {
		return nil
	}
	_, err = r.stdout().Write(read.Data)
	return err
}

func listDirectoryAll(ctx context.Context, repo corev1.RepositoryServiceClient, req *corev1.ListDirectoryRequest) ([]*corev1.TreeEntry, error) {
	entries, _, err := listDirectoryPages(ctx, repo, req)
	return entries, err
}

// listDirectoryPages reads every page of a listing. recursive reports whether
// the server listed recursively, as req.Recursive asks; a server older than
// recursive listing lists one level.
func listDirectoryPages(ctx context.Context, repo corev1.RepositoryServiceClient, req *corev1.ListDirectoryRequest) ([]*corev1.TreeEntry, bool, error) {
	if req == nil {
		req = &corev1.ListDirectoryRequest{}
	}
	pageReq := &corev1.ListDirectoryRequest{
		CommitId:   req.CommitId,
		Path:       req.Path,
		Cursor:     req.Cursor,
		PageSize:   req.PageSize,
		Slice:      req.Slice,
		RootTreeId: req.RootTreeId,
		Recursive:  req.Recursive,
	}
	var entries []*corev1.TreeEntry
	recursive := true
	for {
		page, err := repo.ListDirectory(ctx, pageReq)
		if err != nil {
			return nil, false, err
		}
		entries = append(entries, page.Entries...)
		recursive = recursive && page.Recursive
		if strings.TrimSpace(page.NextCursor) == "" {
			return entries, recursive, nil
		}
		if page.NextCursor == pageReq.Cursor {
			return nil, false, fmt.Errorf("directory pagination did not advance for %s", pageReq.Path)
		}
		pageReq.Cursor = page.NextCursor
	}
}

func (r Runner) homeFSReadScope(ctx context.Context) (UserConfig, *grpc.ClientConn, *corev1.Slice, corev1.RepositoryServiceClient, string, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, nil, nil, nil, "", err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return UserConfig{}, nil, nil, nil, "", err
	}
	callCtx := authContext(ctx, cfg)
	slice, err := r.personalHomeSlice(callCtx, cfg, conn)
	if err != nil {
		conn.Close()
		return UserConfig{}, nil, nil, nil, "", err
	}
	repo := corev1.NewRepositoryServiceClient(conn)
	ref, err := repo.GetRef(callCtx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		conn.Close()
		return UserConfig{}, nil, nil, nil, "", err
	}
	return cfg, conn, slice, repo, ref.CommitId, nil
}

func fsEntryName(entry *corev1.TreeEntry) string {
	if entry == nil {
		return ""
	}
	if entry.Name != "" {
		return entry.Name
	}
	return path.Base(entry.Path)
}

func fsEntryOutputFromProto(entry *corev1.TreeEntry) fsEntryOutput {
	if entry == nil {
		return fsEntryOutput{}
	}
	return fsEntryOutput{
		Path:        entry.Path,
		Name:        fsEntryName(entry),
		Kind:        entryKindName(entry.Kind),
		Mode:        entry.Mode,
		Size:        entry.Size,
		TreeID:      entry.TreeId,
		BlobID:      entry.BlobId,
		ContentHash: entry.ContentHash,
	}
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
