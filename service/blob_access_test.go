package service

import (
	"context"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/internal/storage/memory"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestBlobAccessIsScopedToSliceWithPathHeadFallback(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	uploaderRef := &corev1.SliceRef{Account: "acme", Slice: "uploader"}
	otherRef := &corev1.SliceRef{Account: "acme", Slice: "other"}
	legacyRef := &corev1.SliceRef{Account: "acme", Slice: "legacy"}
	mem.PutSlice(uploaderRef, []string{"/acme/uploader"}, "private")
	mem.PutSlice(otherRef, []string{"/acme/other"}, "private")
	mem.PutSlice(legacyRef, []string{"/acme/legacy"}, "private")
	handlers := New(Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	data := []byte("slice-owned blob\n")
	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Slice: uploaderRef, Data: data})
	if err != nil {
		t.Fatal(err)
	}

	assertBlobStatus(t, ctx, handlers.Blob, uploaderRef, uploaded.ContentHash, "available")
	assertBlobRead(t, ctx, handlers.Blob, uploaderRef, uploaded.ContentHash, data)

	assertBlobStatus(t, ctx, handlers.Blob, otherRef, uploaded.ContentHash, "missing")
	otherStream := &readBlobStreamServerForTest{ctx: ctx}
	err = handlers.Blob.ReadBlobStream(&corev1.ReadBlobStreamRequest{Slice: otherRef, ContentHash: uploaded.ContentHash}, otherStream)
	if status.Code(err) != codes.NotFound || status.Convert(err).Message() != "blob not found" {
		t.Fatalf("other slice ReadBlobStream error = %v, want NotFound blob not found", err)
	}
	if len(otherStream.chunks) != 0 {
		t.Fatalf("other slice received chunks: %#v", otherStream.chunks)
	}

	mem.PutCommitWithFiles("commit_legacy_blob", []storage.FileEntry{{
		Path:        "/acme/legacy/file.txt",
		BlobID:      uploaded.BlobId,
		ContentHash: uploaded.ContentHash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{"/acme/legacy/file.txt"})
	assertBlobStatus(t, ctx, handlers.Blob, legacyRef, uploaded.ContentHash, "available")
	assertBlobRead(t, ctx, handlers.Blob, legacyRef, uploaded.ContentHash, data)
}

func assertBlobStatus(t *testing.T, ctx context.Context, service *BlobService, slice *corev1.SliceRef, contentHash, wantState string) {
	t.Helper()
	response, err := service.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{
		Slice:         slice,
		ContentHashes: []string{contentHash},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Blobs) != 1 || response.Blobs[0].State != wantState {
		t.Fatalf("GetBlobStatus(%s/%s) = %#v, want state %q", slice.Account, slice.Slice, response.Blobs, wantState)
	}
}

func assertBlobRead(t *testing.T, ctx context.Context, service *BlobService, slice *corev1.SliceRef, contentHash string, want []byte) {
	t.Helper()
	stream := &readBlobStreamServerForTest{ctx: ctx}
	if err := service.ReadBlobStream(&corev1.ReadBlobStreamRequest{Slice: slice, ContentHash: contentHash}, stream); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for _, chunk := range stream.chunks {
		got = append(got, chunk.Data...)
	}
	if string(got) != string(want) {
		t.Fatalf("ReadBlobStream(%s/%s) = %q, want %q", slice.Account, slice.Slice, got, want)
	}
}

type readBlobStreamServerForTest struct {
	grpc.ServerStream
	ctx    context.Context
	chunks []*corev1.ReadBlobChunk
}

func (s *readBlobStreamServerForTest) Send(chunk *corev1.ReadBlobChunk) error {
	s.chunks = append(s.chunks, chunk)
	return nil
}

func (s *readBlobStreamServerForTest) SetHeader(metadata.MD) error  { return nil }
func (s *readBlobStreamServerForTest) SendHeader(metadata.MD) error { return nil }
func (s *readBlobStreamServerForTest) SetTrailer(metadata.MD)       {}
func (s *readBlobStreamServerForTest) Context() context.Context     { return s.ctx }
func (s *readBlobStreamServerForTest) SendMsg(any) error            { return nil }
func (s *readBlobStreamServerForTest) RecvMsg(any) error            { return nil }
