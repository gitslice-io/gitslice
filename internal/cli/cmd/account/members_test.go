package account

import (
	"bytes"
	"strings"
	"testing"
)

func TestAccountMembersSayWhoIsWho(t *testing.T) {
	members := []memberOutput{
		{Username: "sunny", SubjectID: "agent_1", Role: "owner", Kind: "agent"},
		{Username: "slices", SubjectID: "user_1", Role: "owner", Kind: "person", DisplayName: "Nic"},
	}
	markMembers(members, "sunny", "agent_1")
	if !members[0].You || members[0].ClaimedAgent || members[1].You || !members[1].ClaimedAgent {
		t.Fatalf("marks = %+v", members)
	}
	var out bytes.Buffer
	printMembers(&out, members)
	want := "sunny\towner\t(agent, you)\nslices\towner\t(Nic, person, claimed this agent)\n"
	if out.String() != want {
		t.Fatalf("output:\n%q\nwant:\n%q", out.String(), want)
	}

	// In an organization nobody "claimed" anything.
	org := []memberOutput{{Username: "slices", SubjectID: "user_1", Role: "owner", Kind: "person"}, {Username: "ci-bot", SubjectID: "agent_2", Role: "writer", Kind: "agent"}}
	markMembers(org, "gitslice", "user_1")
	if org[0].ClaimedAgent || org[1].ClaimedAgent || !org[0].You {
		t.Fatalf("org marks = %+v", org)
	}
}

func TestPrintCIRunners(t *testing.T) {
	var ciBuf bytes.Buffer
	if err := printCIRunners(&ciBuf, ciRunnersOutput{
		Account:     "acme",
		DaemonIDs:   []string{"daemon_1", "daemon_2"},
		DaemonNames: []string{"primary", "backup"},
	}); err != nil {
		t.Fatalf("printCIRunners: %v", err)
	}
	if !strings.Contains(ciBuf.String(), "1. primary (daemon_1)") || !strings.Contains(ciBuf.String(), "2. backup (daemon_2)") {
		t.Fatalf("unexpected ci-runners output:\n%s", ciBuf.String())
	}
}
