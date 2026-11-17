package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type collaboratorsFake struct {
	corev1.UnimplementedChangesetServiceServer

	added, removed *corev1.AddChangesetCollaboratorRequest
	handoff        *corev1.HandoffChangesetRequest
}

func (f *collaboratorsFake) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	return &corev1.Changeset{
		Id: "cs_0123456789abcdef0123456789abcdef", Author: "alice", OriginalAuthor: "bob",
		Collaborators: []string{"carol", "dave"}, Status: "draft",
	}, nil
}

func (f *collaboratorsFake) AddChangesetCollaborator(ctx context.Context, req *corev1.AddChangesetCollaboratorRequest) (*corev1.Changeset, error) {
	f.added = req
	return &corev1.Changeset{Id: req.ChangesetId, Collaborators: []string{req.Username}}, nil
}

func (f *collaboratorsFake) RemoveChangesetCollaborator(ctx context.Context, req *corev1.RemoveChangesetCollaboratorRequest) (*corev1.Changeset, error) {
	f.removed = &corev1.AddChangesetCollaboratorRequest{ChangesetId: req.ChangesetId, Username: req.Username}
	return &corev1.Changeset{Id: req.ChangesetId}, nil
}

func (f *collaboratorsFake) HandoffChangeset(ctx context.Context, req *corev1.HandoffChangesetRequest) (*corev1.Changeset, error) {
	f.handoff = req
	return &corev1.Changeset{Id: req.ChangesetId, Author: req.Username, OriginalAuthor: "alice"}, nil
}

func TestChangesetCollaboratorsAndHandoffCLI(t *testing.T) {
	fake := &collaboratorsFake{}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	corev1.RegisterChangesetServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: lis.Addr().String(), Token: fakeToken, SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		stdout.Reset()
		if err := r.Run(context.Background(), args); err != nil {
			t.Fatalf("gs %v: %v\nstderr: %s", args, err, stderr.String())
		}
		return stdout.String()
	}

	if out := run("cs", "collaborators", "list", "cs_0123456789abcdef0123456789abcdef"); !strings.Contains(out, "carol") || !strings.Contains(out, "dave") {
		t.Fatalf("list output = %q", out)
	}
	var listed struct {
		Collaborators  []string `json:"collaborators"`
		OriginalAuthor string   `json:"original_author"`
	}
	if err := json.Unmarshal([]byte(run("cs", "collaborators", "list", "cs_0123456789abcdef0123456789abcdef", "--json")), &listed); err != nil || len(listed.Collaborators) != 2 || listed.OriginalAuthor != "bob" {
		t.Fatalf("list json = %+v, %v", listed, err)
	}
	run("cs", "collaborators", "add", "cs_0123456789abcdef0123456789abcdef", "erin")
	if fake.added == nil || fake.added.Username != "erin" || fake.added.ChangesetId != "cs_0123456789abcdef0123456789abcdef" {
		t.Fatalf("add request = %+v", fake.added)
	}
	run("cs", "collaborators", "rm", "cs_0123456789abcdef0123456789abcdef", "erin")
	if fake.removed == nil || fake.removed.Username != "erin" {
		t.Fatalf("remove request = %+v", fake.removed)
	}
	if out := run("cs", "handoff", "cs_0123456789abcdef0123456789abcdef", "frank"); !strings.Contains(out, "frank") || !strings.Contains(out, "original author: alice") {
		t.Fatalf("handoff output = %q", out)
	}
	if fake.handoff == nil || fake.handoff.Username != "frank" {
		t.Fatalf("handoff request = %+v", fake.handoff)
	}

	// The schema lists the new commands.
	schema := run("schema")
	for _, want := range []string{"gs cs collaborators add [changeset] <user>", "gs cs collaborators rm [changeset] <user>", "gs cs handoff [changeset] <user>"} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema missing %q", want)
		}
	}
}
