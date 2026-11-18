package main

import (
	"bytes"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/postgres"
)

func TestAdminCommandsValidateFlagsAndConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMain([]string{"rebuild-indexes"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("rebuild-indexes without --yes: code=%d stderr=%q", code, stderr.String())
	}

	stderr.Reset()
	if code := runMain([]string{"gc", "--json"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "dry_run_required") {
		t.Fatalf("gc without --dry-run: code=%d stderr=%q", code, stderr.String())
	}

	var reportOut bytes.Buffer
	printAdminGCReport(&reportOut, postgres.GCReport{
		BlobCount:          4,
		ReachableBlobCount: 3,
		OrphanBlobCount:    1,
		OrphanBlobs:        []string{"blob_1"},
	})
	if !strings.Contains(reportOut.String(), "orphan blob samples:\n  blob_1") {
		t.Fatalf("report output = %q", reportOut.String())
	}
}
