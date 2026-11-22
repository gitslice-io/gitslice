// Package grpccomp registers a pooled zstd gRPC compressor for bulk RPCs
// (such as UploadBlobs and ImportGitCommitBatch). Using zstd SpeedFastest
// avoids the CPU bottleneck of Go's standard gzip compressor on multi-gigabyte
// repository imports while achieving equal or better compression ratios.
package grpccomp

import (
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/grpc/encoding"
)

// ZstdName is the gRPC encoding name registered by this package.
const ZstdName = "zstd"

func init() {
	encoding.RegisterCompressor(&zstdCompressor{})
}

type zstdCompressor struct {
	encPool sync.Pool
	decPool sync.Pool
}

func (c *zstdCompressor) Name() string {
	return ZstdName
}

func (c *zstdCompressor) Compress(w io.Writer) (io.WriteCloser, error) {
	var enc *zstd.Encoder
	if v := c.encPool.Get(); v != nil {
		enc = v.(*zstd.Encoder)
		enc.Reset(w)
	} else {
		var err error
		enc, err = zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
		if err != nil {
			return nil, err
		}
	}
	return &pooledZstdWriter{enc: enc, pool: &c.encPool}, nil
}

func (c *zstdCompressor) Decompress(r io.Reader) (io.Reader, error) {
	var dec *zstd.Decoder
	if v := c.decPool.Get(); v != nil {
		dec = v.(*zstd.Decoder)
		if err := dec.Reset(r); err != nil {
			return nil, err
		}
	} else {
		var err error
		dec, err = zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, err
		}
	}
	return &pooledZstdReader{dec: dec, pool: &c.decPool}, nil
}

type pooledZstdWriter struct {
	enc  *zstd.Encoder
	pool *sync.Pool
}

func (w *pooledZstdWriter) Write(p []byte) (int, error) {
	return w.enc.Write(p)
}

func (w *pooledZstdWriter) Close() error {
	err := w.enc.Close()
	w.enc.Reset(io.Discard)
	w.pool.Put(w.enc)
	w.enc = nil
	return err
}

type pooledZstdReader struct {
	dec  *zstd.Decoder
	pool *sync.Pool
	done bool
}

func (r *pooledZstdReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	n, err := r.dec.Read(p)
	if err == io.EOF {
		r.done = true
		_ = r.dec.Reset(nil)
		r.pool.Put(r.dec)
		r.dec = nil
	}
	return n, err
}
