package service

import (
	"bytes"
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func gitIDTestService(t *testing.T) (*BlobService, *memory.Stores, *recordingObjectStore, context.Context) {
	t.Helper()
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")
	objects := newRecordingObjectStore()
	blob := &BlobService{Auth: mem.Auth, Blobs: mem.Blobs, Slices: mem.Slices, ObjectStore: objects}
	return blob, mem, objects, authctx.WithSubjectID(context.Background(), "user_alice")
}

func TestUploadBlobRecordsTheGitBlobID(t *testing.T) {
	blob, mem, _, ctx := gitIDTestService(t)
	data := []byte("package payment\n")
	res, err := blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"}, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := mem.Blobs.GitBlobIDs(ctx, []string{res.ContentHash})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids[res.ContentHash], objectid.GitBlobID(data); got != want || got == "" {
		t.Fatalf("recorded git id %q, want %q", got, want)
	}
}

func TestUploadBlobStreamRecordsTheGitBlobIDOnlyWhenTheSizeIsDeclared(t *testing.T) {
	data := []byte("streamed, with a size\n")
	for _, declared := range []bool{true, false} {
		blob, mem, _, ctx := gitIDTestService(t)
		init := &corev1.UploadBlobInit{Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"}, ContentHash: objectid.RawContentHash(data)}
		if declared {
			init.Size = int64Ptr(int64(len(data)))
		}
		stream := &uploadBlobStreamServerForTest{ctx: ctx, chunks: []*corev1.UploadBlobChunk{
			{Payload: &corev1.UploadBlobChunk_Init{Init: init}},
			{Payload: &corev1.UploadBlobChunk_Data{Data: data[:5]}},
			{Payload: &corev1.UploadBlobChunk_Data{Data: data[5:]}},
		}}
		if err := blob.UploadBlobStream(stream); err != nil {
			t.Fatal(err)
		}
		ids, _ := mem.Blobs.GitBlobIDs(ctx, []string{stream.response.ContentHash})
		got := ids[stream.response.ContentHash]
		if declared && got != objectid.GitBlobID(data) {
			t.Fatalf("declared size: git id %q, want %q", got, objectid.GitBlobID(data))
		}
		if !declared && got != "" {
			t.Fatalf("no size declared, yet a git id %q was recorded", got)
		}
		// Either way, nothing is left for the backfill once it has run.
		if _, failed, err := blob.BackfillGitBlobIDs(ctx, 10, 2); err != nil || failed != 0 {
			t.Fatalf("backfill: failed=%d err=%v", failed, err)
		}
		ids, _ = mem.Blobs.GitBlobIDs(ctx, []string{stream.response.ContentHash})
		if ids[stream.response.ContentHash] != objectid.GitBlobID(data) {
			t.Fatalf("after the backfill, git id %q, want %q", ids[stream.response.ContentHash], objectid.GitBlobID(data))
		}
	}
}

func TestBackfillGitBlobIDs(t *testing.T) {
	blob, mem, objects, ctx := gitIDTestService(t)
	files := map[string][]byte{"old one\n": nil, "": nil, "a larger file\n": bytes.Repeat([]byte("x"), 300_000)}
	want := map[string]string{}
	for content, big := range files {
		data := []byte(content)
		if big != nil {
			data = big
		}
		hash := objectid.RawContentHash(data)
		key := filesystem.BlobKey(hash)
		if err := objects.Put(ctx, key, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		// Uploaded before git ids existed: a record, and no id.
		if err := mem.Blobs.Upsert(ctx, objectid.BlobID(data), hash, int64(len(data)), key); err != nil {
			t.Fatal(err)
		}
		want[hash] = objectid.GitBlobID(data)
	}
	// A record whose file is gone is counted, not fatal, and is retried later.
	missing := []byte("lost\n")
	if err := mem.Blobs.Upsert(ctx, objectid.BlobID(missing), objectid.RawContentHash(missing), int64(len(missing)), "blobs/gone"); err != nil {
		t.Fatal(err)
	}

	done, failed, err := blob.BackfillGitBlobIDs(ctx, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	if done != len(want) || failed != 1 {
		t.Fatalf("done=%d failed=%d, want %d and 1", done, failed, len(want))
	}
	hashes := make([]string, 0, len(want))
	for hash := range want {
		hashes = append(hashes, hash)
	}
	got, _ := mem.Blobs.GitBlobIDs(ctx, hashes)
	for hash, id := range want {
		if got[hash] != id {
			t.Errorf("git id for %s = %q, want %q", hash, got[hash], id)
		}
	}
	left, _ := mem.Blobs.ListMissingGitBlobIDs(ctx, 10)
	if len(left) != 1 || left[0].StorageLocation != "blobs/gone" {
		t.Fatalf("expected only the unreadable blob to remain, got %#v", left)
	}
}
