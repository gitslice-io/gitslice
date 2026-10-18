package cli

import (
	"context"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// changesetStatusClient answers GetChangeset from a status table, with an
// error code for the ids that have one.
type changesetStatusClient struct {
	corev1.ChangesetServiceClient
	statuses map[string]string
	failures map[string]codes.Code
	calls    int
}

func (c *changesetStatusClient) GetChangeset(_ context.Context, req *corev1.GetChangesetRequest, _ ...grpc.CallOption) (*corev1.Changeset, error) {
	c.calls++
	if code, ok := c.failures[req.ChangesetId]; ok {
		return nil, grpcstatus.Error(code, "cannot read changeset")
	}
	status, ok := c.statuses[req.ChangesetId]
	if !ok {
		return nil, grpcstatus.Error(codes.NotFound, "changeset not found")
	}
	return &corev1.Changeset{Id: req.ChangesetId, Status: status}, nil
}

func TestStackLanded(t *testing.T) {
	entry := func(id, status string) *corev1.ChangesetStackEntry {
		e := &corev1.ChangesetStackEntry{ChangesetId: id}
		if status != "" {
			e.Changeset = &corev1.Changeset{Id: id, Status: status}
		}
		return e
	}
	cases := []struct {
		name      string
		stack     string // the server's status for the tree
		entries   []*corev1.ChangesetStackEntry
		statuses  map[string]string
		failures  map[string]codes.Code
		current   string
		want      bool
		wantErr   codes.Code
		wantCalls int
	}{
		{name: "landed changeset", stack: "closed", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "submitted")}, current: "cs_a", want: true},
		{name: "whole tree landed", stack: "closed", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "submitted"), entry("cs_b", "submitted")}, current: "cs_b", want: true},
		{name: "abandoned sibling", stack: "closed", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "submitted"), entry("cs_b", "abandoned")}, current: "cs_a", want: true},
		{name: "tree still open", stack: "open", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "submitted"), entry("cs_b", "draft")}, current: "cs_a"},
		{name: "own changeset abandoned", stack: "closed", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "abandoned")}, current: "cs_a"},
		{name: "no own changeset", stack: "closed", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "submitted")}, want: true},
		{name: "empty tree", stack: "closed", current: "cs_a"},
		{name: "status read from the server", stack: "closed", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "")}, statuses: map[string]string{"cs_a": "submitted"}, current: "cs_a", want: true, wantCalls: 1},
		{name: "abandoned read from the server", stack: "closed", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "")}, statuses: map[string]string{"cs_a": "abandoned"}, current: "cs_a", wantCalls: 1},
		// An open tree is not read at all: the server's status decides.
		{name: "open tree costs no reads", stack: "open", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "")}, statuses: map[string]string{"cs_a": "submitted"}, current: "cs_a"},
		// A changeset this identity cannot read is not a reason to fail the sync.
		{name: "own changeset gone", stack: "closed", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "")}, current: "cs_a", wantCalls: 1},
		{name: "own changeset not visible", stack: "closed", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "")}, failures: map[string]codes.Code{"cs_a": codes.PermissionDenied}, current: "cs_a", wantCalls: 1},
		{name: "server unavailable", stack: "closed", entries: []*corev1.ChangesetStackEntry{entry("cs_a", "")}, failures: map[string]codes.Code{"cs_a": codes.Unavailable}, current: "cs_a", wantErr: codes.Unavailable, wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &changesetStatusClient{statuses: tc.statuses, failures: tc.failures}
			got, err := stackLanded(context.Background(), client, &corev1.ChangesetStack{Id: "stk_1", Status: tc.stack, Entries: tc.entries}, tc.current)
			if grpcstatus.Code(err) != tc.wantErr {
				t.Fatalf("stackLanded error = %v, want code %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("stackLanded = %v, want %v", got, tc.want)
			}
			if client.calls != tc.wantCalls {
				t.Fatalf("stackLanded read %d changesets, want %d", client.calls, tc.wantCalls)
			}
		})
	}
	if got, err := stackLanded(context.Background(), &changesetStatusClient{}, nil, "cs_a"); got || err != nil {
		t.Fatalf("stackLanded of no tree = %v, %v", got, err)
	}
}
