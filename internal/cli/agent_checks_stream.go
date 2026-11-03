package cli

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

// A check's tree in one call: a Checkout of the patchset's result tree
// (CheckoutRequest.root_tree_id) brings the listing and the contents at once,
// where walking the tree took a ListDirectory per folder and a ReadFile per
// file, about 1,400 requests for the Gitslice tree on a runner with an empty
// cache (a Cloudflare runner always starts with one).

// checkoutStreamer is a repository client that can stream a checkout.
type checkoutStreamer interface {
	Checkout(ctx context.Context, in *corev1.CheckoutRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[corev1.CheckoutResponse], error)
}

// errNoTreeCheckout is a server that cannot check out a tree in one call
// (older than tree checkouts): the caller walks the tree instead.
var errNoTreeCheckout = errors.New("the server cannot check out a tree in one call")

// streamTree checks out the logical prefixes of rootTreeID into plan, putting
// the contents it is sent into cache. Files it was not sent (too large, or past
// the server's total) stay in plan without content, for ReadFile.
func (c checkTreeRepoClient) streamTree(ctx context.Context, rootTreeID string, prefixes []string, plan *checkMaterializePlan, cache *clientcache.ObjectCache) error {
	streamer, ok := c.inner.(checkoutStreamer)
	if !ok || cache == nil {
		return errNoTreeCheckout
	}
	req := &corev1.CheckoutRequest{RootTreeId: rootTreeID, IncludeContent: true}
	for _, prefix := range prefixes {
		req.Paths = append(req.Paths, c.storagePath(prefix))
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
				logical := c.logicalPath(entry.GetPath())
				switch entry.GetKind() {
				case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
					if err := plan.addDir(logical); err != nil {
						return err
					}
				case corev1.EntryKind_ENTRY_KIND_FILE:
					mapped := &corev1.TreeEntry{
						Path: logical, Name: entry.GetName(), Kind: entry.GetKind(), Mode: entry.GetMode(),
						BlobId: entry.GetBlobId(), Size: entry.GetSize(), ContentHash: entry.GetContentHash(),
					}
					if err := plan.addFile(mapped); err != nil {
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
	// A prefix that is a folder is made even when it is empty.
	for _, prefix := range prefixes {
		if !files[prefix] {
			if err := plan.addDir(prefix); err != nil {
				return err
			}
		}
	}
	return nil
}

func treeCheckoutError(err error) error {
	switch grpcstatus.Code(err) {
	case codes.Unimplemented, codes.InvalidArgument:
		return errNoTreeCheckout
	}
	return err
}
