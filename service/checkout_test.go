package service

import (
	"context"
	"reflect"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type checkoutStream struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*corev1.CheckoutResponse
}

func (s *checkoutStream) Context() context.Context { return s.ctx }

func (s *checkoutStream) Send(msg *corev1.CheckoutResponse) error {
	s.msgs = append(s.msgs, msg)
	return nil
}

// TestRepositoryCheckoutStreamsSliceListingAndContent: a checkout sends the
// slice and commit first, then everything under the slice's included paths
// (nothing outside them), then, when asked, each distinct content once.
func TestRepositoryCheckoutStreamsSliceListingAndContent(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	same, other := []byte("same\n"), []byte("other\n")
	for _, data := range [][]byte{same, other} {
		mem.PutObject(filesystem.BlobKey(objectid.RawContentHash(data)), data)
	}
	file := func(p string, data []byte) storage.FileEntry {
		return storage.FileEntry{Path: p, BlobID: objectid.BlobID(data), ContentHash: objectid.RawContentHash(data), Mode: 0o100644, Size: int64(len(data))}
	}
	mem.PutCommitWithFiles("commit_co", []storage.FileEntry{
		file("/acme/co/a.txt", same),
		file("/acme/co/src/b.txt", same),
		file("/acme/co/src/c.txt", other),
		file("/acme/elsewhere/d.txt", other),
	}, nil)
	ref := &corev1.SliceRef{Account: "acme", Slice: "co"}
	mem.PutSlice(ref, []string{"/acme/co"}, "private")

	checkout := func(includeContent bool) ([]string, map[string]string) {
		t.Helper()
		stream := &checkoutStream{ctx: ctx}
		if err := handlers.Repository.Checkout(&corev1.CheckoutRequest{Slice: ref, CommitId: "commit_co", IncludeContent: includeContent}, stream); err != nil {
			t.Fatal(err)
		}
		header := stream.msgs[0].GetHeader()
		if header == nil || header.GetCommitId() != "commit_co" || header.GetSlice().GetRef().GetSlice() != "co" {
			t.Fatalf("first message = %v, want the header", stream.msgs[0])
		}
		var paths []string
		contents := map[string]string{}
		for _, msg := range stream.msgs[1:] {
			for _, entry := range msg.GetEntries().GetEntries() {
				paths = append(paths, entry.GetPath())
			}
			for _, content := range msg.GetContents().GetContents() {
				if _, twice := contents[content.GetContentHash()]; twice {
					t.Fatalf("content %s sent twice", content.GetContentHash())
				}
				contents[content.GetContentHash()] = string(content.GetData())
			}
		}
		return paths, contents
	}

	paths, contents := checkout(true)
	wantPaths := []string{"/acme/co/a.txt", "/acme/co/src", "/acme/co/src/b.txt", "/acme/co/src/c.txt"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %v, want %v", paths, wantPaths)
	}
	wantContents := map[string]string{objectid.RawContentHash(same): "same\n", objectid.RawContentHash(other): "other\n"}
	if !reflect.DeepEqual(contents, wantContents) {
		t.Fatalf("contents = %v, want %v", contents, wantContents)
	}
	if paths, contents := checkout(false); !reflect.DeepEqual(paths, wantPaths) || len(contents) != 0 {
		t.Fatalf("without content: paths = %v, contents = %v", paths, contents)
	}
}

// TestRepositoryCheckoutOfATree: a CI checkout names a tree and paths
// (folders or files) and gets their listing and contents in one call, each
// path authorized as a ReadFile of it would be.
func TestRepositoryCheckoutOfATree(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := authctx.WithSubjectID(context.Background(), "user_alice")
	a, b := []byte("a\n"), []byte("b\n")
	for _, data := range [][]byte{a, b} {
		mem.PutObject(filesystem.BlobKey(objectid.RawContentHash(data)), data)
	}
	file := func(p string, data []byte) storage.FileEntry {
		return storage.FileEntry{Path: p, BlobID: objectid.BlobID(data), ContentHash: objectid.RawContentHash(data), Mode: 0o100644, Size: int64(len(data))}
	}
	mem.PutCommitWithFiles("commit_tree", []storage.FileEntry{
		file("/acme/co/src/a.txt", a),
		file("/acme/co/top.txt", b),
		file("/acme/co/other/x.txt", b),
	}, nil)
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "co"}, []string{"/acme/co"}, "private")
	root, err := mem.Repository.RootTreeForCommit(alice, "commit_tree")
	if err != nil {
		t.Fatal(err)
	}

	stream := &checkoutStream{ctx: alice}
	if err := handlers.Repository.Checkout(&corev1.CheckoutRequest{RootTreeId: root, Paths: []string{"/acme/co/src", "/acme/co/top.txt"}, IncludeContent: true}, stream); err != nil {
		t.Fatal(err)
	}
	if stream.msgs[0].GetHeader() == nil {
		t.Fatalf("first message = %v, want the header", stream.msgs[0])
	}
	var paths []string
	contents := map[string]string{}
	for _, msg := range stream.msgs[1:] {
		for _, entry := range msg.GetEntries().GetEntries() {
			paths = append(paths, entry.GetPath())
		}
		for _, content := range msg.GetContents().GetContents() {
			contents[content.GetContentHash()] = string(content.GetData())
		}
	}
	if want := []string{"/acme/co/src/a.txt", "/acme/co/top.txt"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if want := map[string]string{objectid.RawContentHash(a): "a\n", objectid.RawContentHash(b): "b\n"}; !reflect.DeepEqual(contents, want) {
		t.Fatalf("contents = %v, want %v", contents, want)
	}

	// Someone who may not read the account gets nothing, and paths are required.
	mallory := authctx.WithSubjectID(context.Background(), "user_mallory")
	if err := handlers.Repository.Checkout(&corev1.CheckoutRequest{RootTreeId: root, Paths: []string{"/acme/co/src"}}, &checkoutStream{ctx: mallory}); err == nil {
		t.Fatal("an outsider checked out a private tree")
	}
	if err := handlers.Repository.Checkout(&corev1.CheckoutRequest{RootTreeId: root}, &checkoutStream{ctx: alice}); err == nil {
		t.Fatal("a tree checkout without paths should be refused")
	}
}
