package cli

import (
	"bytes"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// gs status shows the dependency tree once, with its state on the header
// line, so "status:" is only ever the workspace's own changes.
func TestPrintWorkspaceStackOnce(t *testing.T) {
	stack := &corev1.ChangesetStack{
		Id:            "stk_1",
		Status:        "closed",
		ActiveEntryId: "cs_old",
		Entries:       []*corev1.ChangesetStackEntry{{ChangesetId: "cs_1", State: "landed"}},
	}
	var b bytes.Buffer
	printWorkspaceStack(&b, stack, "cs_1")
	got := b.String()
	want := "dependencies: stk_1 (closed)\nactive: 1\nchangesets:\n  1 landed\n"
	if got != want {
		t.Fatalf("printWorkspaceStack =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "status:") {
		t.Fatalf("tree state printed as status:\n%s", got)
	}
}
