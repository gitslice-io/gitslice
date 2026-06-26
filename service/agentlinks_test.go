package service

import (
	"context"
	"testing"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestRewriteAgentFileLinks(t *testing.T) {
	base := linkRewriteContext{account: "acme", slug: "payments", seq: 5}
	tests := []struct {
		name string
		text string
		rc   linkRewriteContext
		want string
	}{
		{
			name: "non-gsfile link untouched",
			text: "See [Go](https://go.dev).",
			rc:   base,
			want: "See [Go](https://go.dev).",
		},
		{
			name: "plain prose untouched",
			text: "No file references here.",
			rc:   base,
			want: "No file references here.",
		},
		{
			name: "slice fallback without patchsets",
			text: "Open [agent file](gsfile:internal/cli/agent.go).",
			rc:   base,
			want: "Open [agent file](/slices/acme/payments?path=internal%2Fcli%2Fagent.go).",
		},
		{
			name: "changeset hit in owning patchset",
			text: "Open [agent file](gsfile:internal/cli/agent.go).",
			rc: linkRewriteContext{
				account: "acme",
				slug:    "payments",
				seq:     2,
				patchsets: []*corev1.Patchset{
					{
						Id:                       "ps1",
						ChangesetId:              "cs1",
						ChangedPaths:             []string{"internal/cli/agent.go"},
						AuthoringConversationSeq: 3,
					},
				},
			},
			want: "Open [agent file](/cs/cs1?to=ps1&file=internal%2Fcli%2Fagent.go).",
		},
		{
			name: "owning patchset selected by smallest cutoff at or after event seq",
			text: "[a](gsfile:a.go) [b](gsfile:b.go)",
			rc: linkRewriteContext{
				account: "acme",
				slug:    "payments",
				seq:     11,
				patchsets: []*corev1.Patchset{
					{Id: "ps3", ChangesetId: "cs3", ChangedPaths: []string{"a.go"}, AuthoringConversationSeq: 30},
					{Id: "ps2", ChangesetId: "cs2", ChangedPaths: []string{"b.go"}, AuthoringConversationSeq: 20},
					{Id: "ps1", ChangesetId: "cs1", ChangedPaths: []string{"a.go"}, AuthoringConversationSeq: 10},
				},
			},
			want: "[a](/slices/acme/payments?path=a.go) [b](/cs/cs2?to=ps2&file=b.go)",
		},
		{
			name: "slice fallback carries line fragment",
			text: "Open [line](gsfile:src/main.go#L42).",
			rc:   base,
			want: "Open [line](/slices/acme/payments?path=src%2Fmain.go#L42).",
		},
		{
			name: "changeset hit carries line range fragment",
			text: "Open [line](gsfile:src/main.go#L42-L60).",
			rc: linkRewriteContext{
				account: "acme",
				slug:    "payments",
				seq:     5,
				patchsets: []*corev1.Patchset{
					{Id: "ps1", ChangesetId: "cs1", ChangedPaths: []string{"src/main.go"}, AuthoringConversationSeq: 5},
				},
			},
			want: "Open [line](/cs/cs1?to=ps1&file=src%2Fmain.go#L42-L60).",
		},
		{
			name: "label with spaces and special chars preserved",
			text: "Review [agent file *now*?](gsfile:agent.go).",
			rc:   base,
			want: "Review [agent file *now*?](/slices/acme/payments?path=agent.go).",
		},
		{
			name: "multiple gsfile links in one string",
			text: "[one](gsfile:changed.go) and [two](gsfile:other.go)",
			rc: linkRewriteContext{
				account: "acme",
				slug:    "payments",
				seq:     5,
				patchsets: []*corev1.Patchset{
					{Id: "ps1", ChangesetId: "cs1", ChangedPaths: []string{"changed.go"}, AuthoringConversationSeq: 5},
				},
			},
			want: "[one](/cs/cs1?to=ps1&file=changed.go) and [two](/slices/acme/payments?path=other.go)",
		},
		{
			name: "image link untouched",
			text: "![alt](gsfile:image.png) [file](gsfile:file.go)",
			rc:   base,
			want: "![alt](gsfile:image.png) [file](/slices/acme/payments?path=file.go)",
		},
		{
			name: "leading dot slash trimmed",
			text: "Open [file](gsfile:./file.go).",
			rc:   base,
			want: "Open [file](/slices/acme/payments?path=file.go).",
		},
		{
			name: "absolute path unchanged",
			text: "Open [file](gsfile:/etc/passwd).",
			rc:   base,
			want: "Open [file](gsfile:/etc/passwd).",
		},
		{
			name: "missing account unchanged",
			text: "Open [file](gsfile:file.go).",
			rc:   linkRewriteContext{slug: "payments", seq: 5},
			want: "Open [file](gsfile:file.go).",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rewriteAgentFileLinks(tt.text, tt.rc); got != tt.want {
				t.Fatalf("rewriteAgentFileLinks() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRewriteConversationLinksClonesLinkedEvents(t *testing.T) {
	svc := &AgentService{}
	conv := &corev1.Conversation{
		Id:    "conv1",
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payments"},
	}
	linked := &corev1.ConversationEvent{Seq: 1, Text: "Open [file](gsfile:file.go)."}
	plain := &corev1.ConversationEvent{Seq: 2, Text: "Plain event."}

	got := svc.rewriteConversationLinks(context.Background(), conv, []*corev1.ConversationEvent{linked, plain})
	if len(got) != 2 {
		t.Fatalf("rewriteConversationLinks returned %d events, want 2", len(got))
	}
	if got[0] == linked {
		t.Fatalf("linked event was not cloned")
	}
	if linked.Text != "Open [file](gsfile:file.go)." {
		t.Fatalf("input linked event was mutated: %q", linked.Text)
	}
	if got[0].Text != "Open [file](/slices/acme/payments?path=file.go)." {
		t.Fatalf("rewritten linked text = %q", got[0].Text)
	}
	if got[1] != plain {
		t.Fatalf("plain event should pass through without cloning")
	}
}
