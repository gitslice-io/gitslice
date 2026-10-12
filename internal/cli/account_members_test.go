package cli

import (
	"bytes"
	"testing"
)

func TestAccountMembersSayWhoIsWho(t *testing.T) {
	members := []accountMemberOutput{
		{Username: "sunny", SubjectID: "agent_1", Role: "owner", Kind: "agent"},
		{Username: "slices", SubjectID: "user_1", Role: "owner", Kind: "person", DisplayName: "Nic"},
	}
	markAccountMembers(members, "sunny", "agent_1")
	if !members[0].You || members[0].ClaimedAgent || members[1].You || !members[1].ClaimedAgent {
		t.Fatalf("marks = %+v", members)
	}
	var out bytes.Buffer
	(Runner{Stdout: &out}).printAccountMembers(members)
	want := "sunny\towner\t(agent, you)\nslices\towner\t(Nic, person, claimed this agent)\n"
	if out.String() != want {
		t.Fatalf("output:\n%q\nwant:\n%q", out.String(), want)
	}

	// In an organization nobody "claimed" anything.
	org := []accountMemberOutput{{Username: "slices", SubjectID: "user_1", Role: "owner", Kind: "person"}, {Username: "ci-bot", SubjectID: "agent_2", Role: "writer", Kind: "agent"}}
	markAccountMembers(org, "gitslice", "user_1")
	if org[0].ClaimedAgent || org[1].ClaimedAgent || !org[0].You {
		t.Fatalf("org marks = %+v", org)
	}
}
