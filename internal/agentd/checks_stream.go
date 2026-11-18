package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding/gzip"
	grpcstatus "google.golang.org/grpc/status"
)

type CheckoutStreamer interface {
	Checkout(ctx context.Context, in *corev1.CheckoutRequest, opts ...grpc.CallOption) (corev1.RepositoryService_CheckoutClient, error)
}

var _ CheckoutStreamer = corev1.NewRepositoryServiceClient(nil)

var ErrNoTreeCheckout = errors.New("the server cannot check out a tree in one call")

func (c CheckTreeRepoClient) StreamTree(ctx context.Context, rootTreeID string, prefixes []string, plan *CheckMaterializePlan, cache *clientcache.ObjectCache) error {
	streamer, ok := c.Inner.(CheckoutStreamer)
	if !ok || cache == nil {
		return ErrNoTreeCheckout
	}
	req := &corev1.CheckoutRequest{RootTreeId: rootTreeID, IncludeContent: true}
	for _, prefix := range prefixes {
		req.Paths = append(req.Paths, c.StoragePath(prefix))
	}
	stream, err := streamer.Checkout(ctx, req, grpc.UseCompressor(gzip.Name))
	if err != nil {
		return treeCheckoutError(err)
	}
	files := map[string]bool{}
	for first := true; ; first = false {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if first {
				return treeCheckoutError(err)
			}
			return err
		}
		switch part := msg.GetPart().(type) {
		case *corev1.CheckoutResponse_Entries:
			for _, entry := range part.Entries.GetEntries() {
				logical := c.LogicalPath(entry.GetPath())
				switch entry.GetKind() {
				case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
					if err := plan.AddDir(logical); err != nil {
						return err
					}
				case corev1.EntryKind_ENTRY_KIND_FILE:
					mapped := &corev1.TreeEntry{
						Path: logical, Name: entry.GetName(), Kind: entry.GetKind(), Mode: entry.GetMode(),
						BlobId: entry.GetBlobId(), Size: entry.GetSize(), ContentHash: entry.GetContentHash(),
					}
					if err := plan.AddFile(mapped); err != nil {
						return err
					}
					files[logical] = true
				default:
					return fmt.Errorf("unsupported check tree entry kind for %s: %s", logical, entryKindName(entry.GetKind()))
				}
			}
		case *corev1.CheckoutResponse_Contents:
			for _, content := range part.Contents.GetContents() {
				cached, err := cache.PutBytes(content.GetData())
				if err != nil {
					return err
				}
				if cached.ContentHash != content.GetContentHash() {
					return fmt.Errorf("check tree content hash mismatch: got %s, want %s", cached.ContentHash, content.GetContentHash())
				}
			}
		}
	}
	for _, prefix := range prefixes {
		if !files[prefix] {
			if err := plan.AddDir(prefix); err != nil {
				return err
			}
		}
	}
	return nil
}

func treeCheckoutError(err error) error {
	switch grpcstatus.Code(err) {
	case codes.Unimplemented, codes.InvalidArgument:
		return ErrNoTreeCheckout
	}
	return err
}
