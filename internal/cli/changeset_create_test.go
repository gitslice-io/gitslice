package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRequiredCommandMessageRejectsNonInteractiveEmptyValue(t *testing.T) {
	r := Runner{Stdin: strings.NewReader("ignored\n")}
	value, err := r.requiredCommandMessage(commandOptions{NonInteractive: true}, "", "Title: ", "create requires --message", "Run gs create --message <title>.")
	if err == nil {
		t.Fatal("expected message_required error")
	}
	if value != "" {
		t.Fatalf("value = %q, want empty", value)
	}
	var cmdErr commandError
	if !errors.As(err, &cmdErr) || cmdErr.Code != "message_required" {
		t.Fatalf("error = %#v, want message_required commandError", err)
	}
	value, err = r.requiredCommandMessage(commandOptions{NonInteractive: true}, "  explicit title  ", "Title: ", "create requires --message", "Run gs create --message <title>.")
	if err != nil {
		t.Fatal(err)
	}
	if value != "explicit title" {
		t.Fatalf("value = %q, want trimmed explicit title", value)
	}
}

func TestWriteCaptureDiagnosticsReportsPhaseTimingsAndCounts(t *testing.T) {
	var stderr bytes.Buffer
	r := Runner{Stderr: &stderr}
	r.writeCaptureDiagnostics(captureDiagnostics{
		Result:       "captured",
		ChangedPaths: 7,
		Snapshot: snapshotEditsStats{
			Scan: workspaceScanStats{
				FileCount:    11,
				ByteCount:    2048,
				HashWorkers:  4,
				WalkDuration: 2 * time.Millisecond,
				HashDuration: 3 * time.Millisecond,
			},
			Blobs: blobAttachStats{
				RequiredBlobs:  6,
				AvailableBlobs: 2,
				UploadedBlobs:  4,
				UploadedBytes:  1024,
				UploadWorkers:  4,
				StatusDuration: time.Millisecond,
				UploadDuration: 5 * time.Millisecond,
			},
			DiffDuration: time.Millisecond,
		},
		ChecksDuration: 6 * time.Millisecond,
		UpdateDuration: 7 * time.Millisecond,
		TotalDuration:  25 * time.Millisecond,
	})

	got := stderr.String()
	for _, want := range []string{
		"result=captured",
		"files=11",
		"hashed_bytes=2048",
		"changed_paths=7",
		"unique_blobs=6",
		"remote_hits=2",
		"uploaded_blobs=4",
		"hash_workers=4",
		"upload_workers=4",
		"walk=2ms",
		"hash=3ms",
		"upload=5ms",
		"checks=6ms",
		"update=7ms",
		"total=25ms",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics missing %q:\n%s", want, got)
		}
	}
}
