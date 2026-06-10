package service

import "github.com/gitslice-io/gitslice/internal/metrics"

var (
	blobUploadsTotal = metrics.NewCounter(
		"gitslice_blob_uploads_total",
		"Successful blob uploads.",
	)
	blobUploadBytesTotal = metrics.NewCounter(
		"gitslice_blob_upload_bytes_total",
		"Bytes accepted by successful blob uploads.",
	)
)

func recordBlobUpload(size int) {
	blobUploadsTotal.Inc(nil)
	blobUploadBytesTotal.Add(float64(size), nil)
}
