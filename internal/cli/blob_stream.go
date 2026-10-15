package cli

import (
	"bytes"
	"context"
	"errors"
	"io"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func uploadBlobBytes(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, contentHash string, data []byte) (*corev1.UploadBlobResponse, error) {
	if int64(len(data)) > blobStreamThresholdBytes {
		return uploadBlobReader(ctx, blobClient, sliceRef, contentHash, int64(len(data)), bytes.NewReader(data))
	}
	return blobClient.UploadBlob(ctx, &corev1.UploadBlobRequest{ContentHash: contentHash, Data: data, Slice: sliceRef})
}

func uploadBlobReader(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, contentHash string, size int64, r io.Reader) (*corev1.UploadBlobResponse, error) {
	stream, err := blobClient.UploadBlobStream(ctx)
	if err != nil {
		return nil, err
	}
	init := &corev1.UploadBlobInit{Slice: sliceRef, ContentHash: contentHash}
	if size >= 0 {
		init.Size = &size
	}
	if err := stream.Send(&corev1.UploadBlobChunk{Payload: &corev1.UploadBlobChunk_Init{Init: init}}); err != nil {
		return nil, err
	}
	buf := make([]byte, blobStreamChunkBytes)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if err := stream.Send(&corev1.UploadBlobChunk{Payload: &corev1.UploadBlobChunk_Data{Data: data}}); err != nil {
				return nil, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return stream.CloseAndRecv()
}

func readBlobStreamBytes(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, contentHash string, offset, length int64) ([]byte, error) {
	var b bytes.Buffer
	if err := writeBlobStream(ctx, blobClient, sliceRef, contentHash, offset, length, &b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeBlobStream(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, contentHash string, offset, length int64, dst io.Writer) error {
	stream, err := blobClient.ReadBlobStream(ctx, &corev1.ReadBlobStreamRequest{
		Slice:       sliceRef,
		ContentHash: contentHash,
		Offset:      offset,
		Length:      length,
	})
	if err != nil {
		return err
	}
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(chunk.Data) == 0 {
			continue
		}
		if _, err := dst.Write(chunk.Data); err != nil {
			return err
		}
	}
}

type blobStreamReader struct {
	stream corev1.BlobService_ReadBlobStreamClient
	buf    []byte
}

func newBlobStreamReader(stream corev1.BlobService_ReadBlobStreamClient) *blobStreamReader {
	return &blobStreamReader{stream: stream}
}

func (r *blobStreamReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		chunk, err := r.stream.Recv()
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		r.buf = chunk.Data
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
