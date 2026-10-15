package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"

	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type fsUploadOptions struct {
	Recursive   bool
	Concurrency int
}

type localUploadPlan struct {
	Files           []localUploadFile
	EmptyRemoteDirs []string
}

type localUploadFile struct {
	LocalPath   string
	RemotePath  string
	Mode        uint32
	Size        int64
	ContentHash string
	BlobID      string
}

func (r Runner) runFSUpload(ctx context.Context, opts commandOptions, localPath, remotePath string, uploadOpts fsUploadOptions) error {
	if uploadOpts.Concurrency <= 0 {
		return userError("invalid_args", "upload concurrency must be positive", "Pass --concurrency 1 or higher.")
	}
	cfg, conn, mutator, err := r.homeFileMutator(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	source := localUploadSourcePath(r.cwd(), localPath)
	plan, err := buildLocalUploadPlan(mutator.slice, source, remotePath, uploadOpts.Recursive)
	if err != nil {
		return err
	}
	edits := make([]*corev1.FileEdit, 0, len(plan.Files)+len(plan.EmptyRemoteDirs))
	if len(plan.Files) > 0 {
		fileEdits, err := r.uploadLocalFiles(ctx, cfg, conn, mutator.slice.Ref, plan.Files, uploadOpts.Concurrency)
		if err != nil {
			return err
		}
		edits = append(edits, fileEdits...)
	}
	for _, dir := range plan.EmptyRemoteDirs {
		edits = append(edits, &corev1.FileEdit{Op: "mkdir", Path: dir})
	}
	if len(edits) == 0 {
		return userError("empty_upload", "no files or directories to upload", "Choose a non-empty source or a destination below the home root.")
	}
	return mutator.apply(ctx, opts, "upload", edits)
}

func localUploadSourcePath(cwd, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(cwd, value))
}

func buildLocalUploadPlan(slice *corev1.Slice, source, remotePath string, recursive bool) (localUploadPlan, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return localUploadPlan{}, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return localUploadPlan{}, userError("unsupported_local_file", "local source is a symlink: "+source, "Upload regular files or directories.")
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return localUploadPlan{}, userError("unsupported_local_file", "local source is not a regular file: "+source, "Upload regular files or directories.")
		}
		p, err := normalizeMutationPath(slice, remotePath)
		if err != nil {
			return localUploadPlan{}, err
		}
		return localUploadPlan{Files: []localUploadFile{{
			LocalPath:  source,
			RemotePath: p,
			Mode:       uploadFileMode(info.Mode()),
			Size:       info.Size(),
		}}}, nil
	}
	if !recursive {
		return localUploadPlan{}, userError("recursive_required", "local source is a directory: "+source, "Pass --recursive to upload directories.")
	}
	remoteRoot, err := normalizeHomePath(slice, remotePath, true)
	if err != nil {
		return localUploadPlan{}, err
	}
	root, err := homeSliceRoot(slice)
	if err != nil {
		return localUploadPlan{}, err
	}
	source = filepath.Clean(source)
	plan := localUploadPlan{}
	dirs := map[string]struct{}{}
	nonEmptyDirs := map[string]struct{}{}
	err = filepath.WalkDir(source, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		parent := path.Dir(rel)
		if parent == "." {
			parent = ""
		}
		nonEmptyDirs[parent] = struct{}{}
		if entry.Type()&fs.ModeSymlink != 0 {
			return userError("unsupported_local_file", "local source contains a symlink: "+p, "Upload regular files or directories.")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs[rel] = struct{}{}
			return nil
		}
		if !info.Mode().IsRegular() {
			return userError("unsupported_local_file", "local source contains a non-regular file: "+p, "Upload regular files or directories.")
		}
		remoteFile, err := normalizeMutationPath(slice, joinRemoteUploadPath(remoteRoot, rel))
		if err != nil {
			return err
		}
		plan.Files = append(plan.Files, localUploadFile{
			LocalPath:  p,
			RemotePath: remoteFile,
			Mode:       uploadFileMode(info.Mode()),
			Size:       info.Size(),
		})
		return nil
	})
	if err != nil {
		return localUploadPlan{}, err
	}
	if len(plan.Files) == 0 && len(dirs) == 0 {
		if remoteRoot == root {
			return plan, nil
		}
		dir, err := normalizeMutationPath(slice, remoteRoot)
		if err != nil {
			return localUploadPlan{}, err
		}
		plan.EmptyRemoteDirs = append(plan.EmptyRemoteDirs, dir)
		return plan, nil
	}
	emptyDirs := make([]string, 0, len(dirs))
	for dir := range dirs {
		if _, ok := nonEmptyDirs[dir]; ok {
			continue
		}
		emptyDirs = append(emptyDirs, dir)
	}
	sort.Strings(emptyDirs)
	for _, dir := range emptyDirs {
		remoteDir, err := normalizeMutationPath(slice, joinRemoteUploadPath(remoteRoot, dir))
		if err != nil {
			return localUploadPlan{}, err
		}
		plan.EmptyRemoteDirs = append(plan.EmptyRemoteDirs, remoteDir)
	}
	sort.Slice(plan.Files, func(i, j int) bool {
		return plan.Files[i].RemotePath < plan.Files[j].RemotePath
	})
	return plan, nil
}

func joinRemoteUploadPath(root, rel string) string {
	if rel == "" || rel == "." {
		return root
	}
	return path.Join(root, rel)
}

func uploadFileMode(mode fs.FileMode) uint32 {
	if mode&0o111 != 0 {
		return 0o100755
	}
	return 0o100644
}

func (r Runner) uploadLocalFiles(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, sliceRef *corev1.SliceRef, files []localUploadFile, concurrency int) ([]*corev1.FileEdit, error) {
	files = append([]localUploadFile(nil), files...)
	if err := hashLocalUploadFiles(ctx, files, boundedUploadConcurrency(concurrency, len(files))); err != nil {
		return nil, err
	}
	blobClient := corev1.NewBlobServiceClient(conn)
	callCtx := authContext(ctx, cfg)
	known, err := remoteBlobRecords(callCtx, blobClient, sliceRef, files)
	if err != nil {
		return nil, err
	}
	missingByHash := map[string]localUploadFile{}
	for _, file := range files {
		if record, ok := known[file.ContentHash]; ok && record.Id != "" && record.State != "missing" {
			continue
		}
		if _, ok := missingByHash[file.ContentHash]; !ok {
			missingByHash[file.ContentHash] = file
		}
	}
	if len(missingByHash) > 0 {
		uploaded, err := uploadMissingLocalBlobs(callCtx, blobClient, sliceRef, missingByHash, boundedUploadConcurrency(concurrency, len(missingByHash)))
		if err != nil {
			return nil, err
		}
		for hash, record := range uploaded {
			known[hash] = record
		}
	}
	edits := make([]*corev1.FileEdit, 0, len(files))
	for _, file := range files {
		record, ok := known[file.ContentHash]
		if !ok || record.Id == "" {
			return nil, fmt.Errorf("blob upload did not return blob id for %s", file.LocalPath)
		}
		edits = append(edits, &corev1.FileEdit{
			Op:          "upsert",
			Path:        file.RemotePath,
			BlobId:      record.Id,
			ContentHash: file.ContentHash,
			Mode:        file.Mode,
		})
	}
	return edits, nil
}

func hashLocalUploadFiles(ctx context.Context, files []localUploadFile, concurrency int) error {
	type result struct {
		index       int
		contentHash string
		size        int64
		err         error
	}
	progress := startProgress(ctx, "hashing", "files", len(files))
	defer progress.Finish()
	jobs := make(chan int)
	results := make(chan result, len(files))
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				select {
				case <-ctx.Done():
					results <- result{index: index, err: ctx.Err()}
					continue
				default:
				}
				f, err := os.Open(files[index].LocalPath)
				if err != nil {
					results <- result{index: index, err: err}
					continue
				}
				hash, size, hashErr := objectid.RawContentHashReader(f)
				closeErr := f.Close()
				if hashErr != nil {
					results <- result{index: index, err: hashErr}
					continue
				}
				if closeErr != nil {
					results <- result{index: index, err: closeErr}
					continue
				}
				results <- result{index: index, contentHash: hash, size: size}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range files {
			select {
			case <-ctx.Done():
				return
			case jobs <- i:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	for result := range results {
		progress.Add(1)
		if result.err != nil {
			return result.err
		}
		files[result.index].ContentHash = result.contentHash
		files[result.index].Size = result.size
	}
	return ctx.Err()
}
