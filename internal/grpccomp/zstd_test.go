package grpccomp

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"google.golang.org/grpc/encoding"
)

func TestZstdCompressorRoundTrip(t *testing.T) {
	comp := encoding.GetCompressor(ZstdName)
	if comp == nil {
		t.Fatalf("expected compressor %q to be registered", ZstdName)
	}
	payload := []byte(strings.Repeat("gitslice zstd grpc payload line\n", 1024))
	for i := 0; i < 4; i++ {
		var buf bytes.Buffer
		w, err := comp.Compress(&buf)
		if err != nil {
			t.Fatalf("Compress(%d): %v", i, err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("Write(%d): %v", i, err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close(%d): %v", i, err)
		}
		if buf.Len() >= len(payload)/10 {
			t.Fatalf("expected strong compression, got %d bytes from %d", buf.Len(), len(payload))
		}
		r, err := comp.Decompress(&buf)
		if err != nil {
			t.Fatalf("Decompress(%d): %v", i, err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("ReadAll(%d): %v", i, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("round-trip mismatch on iteration %d", i)
		}
	}
}
