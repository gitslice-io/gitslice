package service

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/internal/storage/memory"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestHashVerifyingWriteCloser(t *testing.T) {
	data := []byte("streamed blob data")
	var b bytes.Buffer
	writer := newHashVerifyingWriteCloser(nopWriteCloser{Writer: &b}, objectid.RawContentHash(data), int64Ptr(int64(len(data))))
	if _, err := writer.Write(data[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data[8:]); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != string(data) {
		t.Fatalf("writer stored %q, want %q", got, string(data))
	}
	if writer.ContentHash() != objectid.RawContentHash(data) || writer.BlobID() != objectid.BlobID(data) || writer.Size() != int64(len(data)) {
		t.Fatalf("unexpected writer hashes: content=%s blob=%s size=%d", writer.ContentHash(), writer.BlobID(), writer.Size())
	}
}

func TestHashVerifyingWriteCloserRejectsMismatch(t *testing.T) {
	var b bytes.Buffer
	writer := newHashVerifyingWriteCloser(nopWriteCloser{Writer: &b}, "sha256:not-the-content", nil)
	if _, err := writer.Write([]byte("content")); err != nil {
		t.Fatal(err)
	}
	err := writer.Close()
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Close mismatch error = %v, want InvalidArgument", err)
	}
}

func TestUploadBlobStreamHashMismatchCleansStaging(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")
	objects := newRecordingObjectStore()
	blob := &BlobService{Auth: mem.Auth, Blobs: mem.Blobs, Slices: mem.Slices, ObjectStore: objects}

	data := bytes.Repeat([]byte("bad hash\n"), 128)
	stream := &uploadBlobStreamServerForTest{
		ctx: authctx.WithSubjectID(context.Background(), "user_alice"),
		chunks: []*corev1.UploadBlobChunk{
			{Payload: &corev1.UploadBlobChunk_Init{Init: &corev1.UploadBlobInit{
				Slice:       &corev1.SliceRef{Account: "acme", Slice: "payment"},
				ContentHash: "sha256:not-the-content",
				Size:        int64Ptr(int64(len(data))),
			}}},
			{Payload: &corev1.UploadBlobChunk_Data{Data: data}},
		},
	}
	err := blob.UploadBlobStream(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("UploadBlobStream mismatch error = %v, want InvalidArgument", err)
	}
	if stream.response != nil {
		t.Fatalf("unexpected response on failed upload: %#v", stream.response)
	}
	if objects.hasKeyPrefix("staging/") {
		t.Fatalf("staging object was not cleaned up: %#v", objects.keys())
	}
	if objects.hasKey(filesystem.BlobKey(objectid.RawContentHash(data))) {
		t.Fatalf("final object exists after failed upload: %#v", objects.keys())
	}
	records, err := mem.Blobs.GetByContentHash(context.Background(), []string{objectid.RawContentHash(data)})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("failed upload left blob metadata: %#v", records)
	}
}

func TestUploadBlobStreamAssociatesAuthorizedSlice(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	slice := mem.PutSlice(sliceRef, []string{"/acme/payment"}, "private")
	objects := newRecordingObjectStore()
	blob := &BlobService{Auth: mem.Auth, Blobs: mem.Blobs, Slices: mem.Slices, ObjectStore: objects}
	data := []byte("streamed association\n")
	stream := &uploadBlobStreamServerForTest{
		ctx: authctx.WithSubjectID(context.Background(), "user_alice"),
		chunks: []*corev1.UploadBlobChunk{
			{Payload: &corev1.UploadBlobChunk_Init{Init: &corev1.UploadBlobInit{
				Slice:       sliceRef,
				ContentHash: objectid.RawContentHash(data),
				Size:        int64Ptr(int64(len(data))),
			}}},
			{Payload: &corev1.UploadBlobChunk_Data{Data: data}},
		},
	}
	if err := blob.UploadBlobStream(stream); err != nil {
		t.Fatal(err)
	}
	if stream.response == nil {
		t.Fatal("UploadBlobStream returned no response")
	}
	associations, err := mem.Blobs.SliceAssociations(context.Background(), slice.Id, []string{stream.response.ContentHash})
	if err != nil {
		t.Fatal(err)
	}
	if !associations[stream.response.ContentHash] {
		t.Fatalf("slice associations = %#v, want %s", associations, stream.response.ContentHash)
	}
}

type nopWriteCloser struct {
	io.Writer
}

func (w nopWriteCloser) Close() error {
	return nil
}

func int64Ptr(v int64) *int64 {
	return &v
}

type uploadBlobStreamServerForTest struct {
	grpc.ServerStream
	ctx      context.Context
	chunks   []*corev1.UploadBlobChunk
	response *corev1.UploadBlobResponse
}

func (s *uploadBlobStreamServerForTest) Recv() (*corev1.UploadBlobChunk, error) {
	if len(s.chunks) == 0 {
		return nil, io.EOF
	}
	chunk := s.chunks[0]
	s.chunks = s.chunks[1:]
	return chunk, nil
}

func (s *uploadBlobStreamServerForTest) SendAndClose(res *corev1.UploadBlobResponse) error {
	s.response = res
	return nil
}

func (s *uploadBlobStreamServerForTest) SetHeader(metadata.MD) error {
	return nil
}

func (s *uploadBlobStreamServerForTest) SendHeader(metadata.MD) error {
	return nil
}

func (s *uploadBlobStreamServerForTest) SetTrailer(metadata.MD) {}

func (s *uploadBlobStreamServerForTest) Context() context.Context {
	return s.ctx
}

func (s *uploadBlobStreamServerForTest) SendMsg(any) error {
	return nil
}

func (s *uploadBlobStreamServerForTest) RecvMsg(any) error {
	return nil
}

type recordingObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newRecordingObjectStore() *recordingObjectStore {
	return &recordingObjectStore{objects: map[string][]byte{}}
}

func (s *recordingObjectStore) Put(ctx context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	return nil
}

func (s *recordingObjectStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	end := int64(len(data))
	if length > 0 && offset+length < end {
		end = offset + length
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data[offset:end]...))), nil
}

func (s *recordingObjectStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *recordingObjectStore) hasKey(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok
}

func (s *recordingObjectStore) hasKeyPrefix(prefix string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (s *recordingObjectStore) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		keys = append(keys, key)
	}
	return keys
}
