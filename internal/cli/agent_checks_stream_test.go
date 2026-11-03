package cli

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// streamingCheckRepo answers a tree Checkout with canned messages, and counts
// the per-file reads the daemon still makes.
type streamingCheckRepo struct {
	*fakeCheckRepo
	msgs      []*corev1.CheckoutResponse
	gotPaths  []string
	unsupport bool
}

type cannedCheckout struct {
	grpc.ClientStream
	msgs []*corev1.CheckoutResponse
}

func (c *cannedCheckout) Recv() (*corev1.CheckoutResponse, error) {
	if len(c.msgs) == 0 {
		return nil, io.EOF
	}
	msg := c.msgs[0]
	c.msgs = c.msgs[1:]
	return msg, nil
}

func (s *streamingCheckRepo) Checkout(ctx context.Context, in *corev1.CheckoutRequest, opts ...grpc.CallOption) (corev1.RepositoryService_CheckoutClient, error) {
	if s.unsupport {
		return nil, status.Error(codes.InvalidArgument, "slice is required")
	}
	s.gotPaths = in.GetPaths()
	return &cannedCheckout{msgs: s.msgs}, nil
}

func TestMaterializeCheckTreeInOneCheckout(t *testing.T) {
	script := []byte("echo streamed\n")
	big := []byte("not sent inline\n")
	file := func(p string, data []byte) *corev1.TreeEntry {
		e := checkFileEntry(p, 0o100755)
		e.ContentHash = objectid.RawContentHash(data)
		e.Size = int64(len(data))
		return e
	}
	inner := &streamingCheckRepo{
		// The file the server did not send is read the old way.
		fakeCheckRepo: newFakeCheckRepo(nil, map[string][]byte{"/acme/backend/big.bin": big}),
		msgs: []*corev1.CheckoutResponse{
			{Part: &corev1.CheckoutResponse_Header{Header: &corev1.CheckoutHeader{}}},
			{Part: &corev1.CheckoutResponse_Entries{Entries: &corev1.CheckoutEntries{Entries: []*corev1.TreeEntry{
				checkDirEntry("/acme/backend/empty"),
				file("/acme/backend/run.sh", script),
				file("/acme/backend/big.bin", big),
			}}}},
			{Part: &corev1.CheckoutResponse_Contents{Contents: &corev1.CheckoutContents{Contents: []*corev1.CheckoutContent{
				{ContentHash: objectid.RawContentHash(script), Data: script},
			}}}},
		},
	}
	dest := t.TempDir()
	d := newTestCheckDaemon(t)
	if err := d.materializeCheckTree(context.Background(), checkTreeRepoClient{account: "acme", inner: inner}, "tree-1", []string{"backend"}, dest); err != nil {
		t.Fatal(err)
	}
	if len(inner.gotPaths) != 1 || inner.gotPaths[0] != "/acme/backend" {
		t.Fatalf("checkout paths = %v, want the account path", inner.gotPaths)
	}
	assertFileContent(t, filepath.Join(dest, "backend", "run.sh"), "echo streamed\n")
	assertFileContent(t, filepath.Join(dest, "backend", "big.bin"), "not sent inline\n")
	if inner.readCalls != 1 {
		t.Fatalf("%d ReadFile calls, want 1 (only the file not sent)", inner.readCalls)
	}
	if len(inner.listCalls) != 0 {
		t.Fatalf("listed %v; a checkout needs no listing", inner.listCalls)
	}
}

func TestMaterializeCheckTreeWalksOnAnOlderServer(t *testing.T) {
	inner := &streamingCheckRepo{
		unsupport: true,
		fakeCheckRepo: newFakeCheckRepo(map[string][]*corev1.TreeEntry{
			"/acme":         {checkDirEntry("/acme/backend")},
			"/acme/backend": {checkFileEntry("/acme/backend/run.sh", 0o100644)},
		}, map[string][]byte{"/acme/backend/run.sh": []byte("walked\n")}),
	}
	dest := t.TempDir()
	if err := newTestCheckDaemon(t).materializeCheckTree(context.Background(), checkTreeRepoClient{account: "acme", inner: inner}, "tree-1", []string{"backend"}, dest); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(dest, "backend", "run.sh"), "walked\n")
}
