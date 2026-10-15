package service

import (
	"context"
	"io"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	checkoutEntriesPerMessage   = 1000
	checkoutContentMessageBytes = 1 << 20
	checkoutInlineFileBytes     = 4 << 20
	checkoutInlineTotalBytes    = 256 << 20
	checkoutObjectReads         = 64
)

// Checkout streams what a new workspace of a slice needs in one call
// (design/28_benchmarks.md): the slice and the commit, everything under the
// slice's included paths, and, when asked, the content of its files (each
// distinct content once; files up to checkoutInlineFileBytes, up to
// checkoutInlineTotalBytes in all). A checkout over a slow link then costs one
// round trip rather than one each for the slice, the head, the listing and
// the files.
func (s *RepositoryService) Checkout(req *corev1.CheckoutRequest, stream corev1.RepositoryService_CheckoutServer) error {
	ctx := stream.Context()
	if req.GetSlice() == nil {
		return status.Error(codes.InvalidArgument, "slice is required")
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, optionalSubject(ctx), req.GetSlice(), authz.ActionRead)
	if err != nil {
		return err
	}
	commitID := strings.TrimSpace(req.GetCommitId())
	if commitID == "" {
		head, err := s.Repository.GetRef(ctx, storage.DefaultTargetRef)
		if err != nil {
			return grpcError(err)
		}
		commitID = head.CommitId
	}
	rootTreeID, err := s.Repository.RootTreeForCommit(ctx, commitID)
	if err != nil {
		return grpcError(err)
	}
	if err := stream.Send(&corev1.CheckoutResponse{Part: &corev1.CheckoutResponse_Header{Header: &corev1.CheckoutHeader{Slice: slice, CommitId: commitID}}}); err != nil {
		return err
	}

	seen := map[string]bool{}
	var files []storage.TreeEntry
	var batch []*corev1.TreeEntry
	sendEntries := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := stream.Send(&corev1.CheckoutResponse{Part: &corev1.CheckoutResponse_Entries{Entries: &corev1.CheckoutEntries{Entries: batch}}})
		batch = nil
		return err
	}
	for _, included := range slice.GetDefinition().GetIncludedPaths() {
		p, err := repositoryReadPath(included)
		if err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		entries, err := s.Repository.ListTreeAtTree(ctx, rootTreeID, p)
		if err != nil {
			return grpcError(err)
		}
		for _, entry := range entries {
			if seen[entry.Path] {
				continue
			}
			seen[entry.Path] = true
			batch = append(batch, treeEntryFromRepositoryEntry(entry))
			if entry.Kind == "file" {
				files = append(files, entry)
			}
			if len(batch) >= checkoutEntriesPerMessage {
				if err := sendEntries(); err != nil {
					return err
				}
			}
		}
	}
	if err := sendEntries(); err != nil {
		return err
	}
	if !req.GetIncludeContent() {
		return nil
	}
	return s.streamCheckoutContents(ctx, stream, files)
}

// streamCheckoutContents reads the files' objects concurrently and sends
// them in messages of about checkoutContentMessageBytes.
func (s *RepositoryService) streamCheckoutContents(ctx context.Context, stream corev1.RepositoryService_CheckoutServer, files []storage.TreeEntry) error {
	var hashes []string
	chosen := map[string]bool{}
	var total int64
	for _, file := range files {
		if file.ContentHash == "" || file.Size > checkoutInlineFileBytes || chosen[file.ContentHash] {
			continue
		}
		if total+file.Size > checkoutInlineTotalBytes {
			break
		}
		chosen[file.ContentHash] = true
		total += file.Size
		hashes = append(hashes, file.ContentHash)
	}

	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan *corev1.CheckoutContent, checkoutObjectReads)
	readErr := make(chan error, 1)
	go func() {
		group, groupCtx := errgroup.WithContext(readCtx)
		group.SetLimit(checkoutObjectReads)
		for _, hash := range hashes {
			group.Go(func() error {
				rc, err := s.ObjectStore.Get(groupCtx, filesystem.BlobKey(hash), 0, 0)
				if err != nil {
					return grpcError(err)
				}
				defer rc.Close()
				data, err := io.ReadAll(rc)
				if err != nil {
					return grpcError(err)
				}
				select {
				case results <- &corev1.CheckoutContent{ContentHash: hash, Data: data}:
					return nil
				case <-groupCtx.Done():
					return groupCtx.Err()
				}
			})
		}
		readErr <- group.Wait()
		close(results)
	}()

	var batch []*corev1.CheckoutContent
	var batchBytes int
	send := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := stream.Send(&corev1.CheckoutResponse{Part: &corev1.CheckoutResponse_Contents{Contents: &corev1.CheckoutContents{Contents: batch}}})
		batch, batchBytes = nil, 0
		return err
	}
	for content := range results {
		batch = append(batch, content)
		batchBytes += len(content.Data)
		if batchBytes >= checkoutContentMessageBytes {
			if err := send(); err != nil {
				return err
			}
		}
	}
	if err := <-readErr; err != nil {
		return err
	}
	return send()
}
