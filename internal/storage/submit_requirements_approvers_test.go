package storage

import (
	"errors"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestGroupParsingAndNormalization(t *testing.T) {
	cases := []struct {
		raw         string
		wantAccount string
		wantName    string
		wantErr     bool
	}{
		{raw: "@Acme/Backend_Team", wantAccount: "acme", wantName: "backend-team"},
		{raw: "@acme/x", wantAccount: "acme", wantName: "x"},
		{raw: "acme/backend", wantErr: true},
		{raw: "@acme", wantErr: true},
		{raw: "@acme/", wantErr: true},
		{raw: "@/api", wantErr: true},
		{raw: "@acme/a/b", wantErr: true},
		{raw: "@acme/-bad", wantErr: true},
		{raw: "@acme/bad-", wantErr: true},
		{raw: "@acme/bad.name", wantErr: true},
	}
	for _, tc := range cases {
		acct, name, err := ParseGroupRefStrict(tc.raw)
		if tc.wantErr {
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Errorf("ParseGroupRefStrict(%q) = (%q, %q, %v), want ErrInvalid", tc.raw, acct, name, err)
			}
			continue
		}
		if err != nil || acct != tc.wantAccount || name != tc.wantName {
			t.Errorf("ParseGroupRefStrict(%q) = (%q, %q, %v), want (%q, %q, nil)", tc.raw, acct, name, err, tc.wantAccount, tc.wantName)
		}
	}
}

func TestEvaluateSubmitRequirementsWithGroup(t *testing.T) {
	humanAlice := Subject{ID: "user_alice", Kind: "user", ControllerSubjectID: "user_alice"}
	humanBob := Subject{ID: "user_bob", Kind: "user", ControllerSubjectID: "user_bob"}
	humanCarol := Subject{ID: "user_carol", Kind: "user", ControllerSubjectID: "user_carol"}
	agentBob1 := Subject{ID: "agent_bob_1", Kind: "agent", ControllerSubjectID: "user_bob"}
	agentBob2 := Subject{ID: "agent_bob_2", Kind: "agent", ControllerSubjectID: "user_bob"}
	agentCarol := Subject{ID: "agent_carol", Kind: "agent", ControllerSubjectID: "user_carol"}
	unclaimedAgent := Subject{ID: "agent_unclaimed", Kind: "agent", ControllerSubjectID: ""}

	groupMembers := map[string]bool{
		humanBob.ID:       true,
		agentBob1.ID:      true,
		agentBob2.ID:      true,
		agentCarol.ID:     true,
		unclaimedAgent.ID: true,
	}

	reqTwo := &corev1.SubmitRequirements{
		RequiredApprovals: 2,
		Approvers:         "@acme/reviewers",
	}

	// Non-member humanCarol + unclaimed agent + 2 agents controlled by user_bob
	// yield only 1 distinct valid group-member controller approval.
	reason := EvaluateSubmitRequirementsWithGroup(
		reqTwo,
		[]Subject{humanAlice},
		[]Subject{humanCarol, unclaimedAgent, agentBob1, agentBob2},
		nil,
		groupMembers,
	)
	if !strings.HasPrefix(reason, "required approvals not satisfied:") || !strings.Contains(reason, "from @acme/reviewers, has 1") {
		t.Fatalf("got reason %q, want 1 from @acme/reviewers", reason)
	}

	// Adding agentCarol (different controller, member of @acme/reviewers) satisfies 2.
	reason = EvaluateSubmitRequirementsWithGroup(
		reqTwo,
		[]Subject{humanAlice},
		[]Subject{agentBob1, agentCarol},
		nil,
		groupMembers,
	)
	if reason != "" {
		t.Fatalf("got reason %q, want empty", reason)
	}

	// Author's own controller cannot self-approve via group membership.
	reason = EvaluateSubmitRequirementsWithGroup(
		&corev1.SubmitRequirements{RequiredApprovals: 1, Approvers: "@acme/reviewers"},
		[]Subject{agentBob1},
		[]Subject{humanBob, agentBob2},
		nil,
		groupMembers,
	)
	if !strings.HasPrefix(reason, "required approvals not satisfied:") || !strings.Contains(reason, "from @acme/reviewers, has 0") {
		t.Fatalf("same-controller self-approval got %q, want has 0", reason)
	}

	// Empty / nil approver group fails closed even when someone approves.
	for _, m := range []map[string]bool{nil, {}} {
		reason = EvaluateSubmitRequirementsWithGroup(
			&corev1.SubmitRequirements{RequiredApprovals: 1, Approvers: "@acme/reviewers"},
			[]Subject{humanAlice},
			[]Subject{humanBob},
			nil,
			m,
		)
		if !strings.HasPrefix(reason, "required approvals not satisfied:") || !strings.Contains(reason, "from @acme/reviewers, has 0") {
			t.Fatalf("empty group members got %q, want has 0", reason)
		}
	}

	// RequiredHumanApprovals also respects group membership.
	reqHuman := &corev1.SubmitRequirements{
		RequiredApprovals:      1,
		RequiredHumanApprovals: 1,
		Approvers:              "@acme/reviewers",
	}
	// humanCarol is NOT in @acme/reviewers, agentCarol IS in @acme/reviewers -> total 1 (agent) but human 0.
	reason = EvaluateSubmitRequirementsWithGroup(
		reqHuman,
		[]Subject{humanAlice},
		[]Subject{humanCarol, agentCarol},
		nil,
		groupMembers,
	)
	if !strings.HasPrefix(reason, "required human approvals not satisfied:") || !strings.Contains(reason, "has 0") {
		t.Fatalf("non-member human + member agent got %q", reason)
	}

	// humanBob IS in @acme/reviewers -> both total and human gates satisfied.
	reason = EvaluateSubmitRequirementsWithGroup(
		reqHuman,
		[]Subject{humanAlice},
		[]Subject{humanBob},
		nil,
		groupMembers,
	)
	if reason != "" {
		t.Fatalf("member human got reason %q, want empty", reason)
	}
}

func TestSubmitRequirementsHashWithApprovers(t *testing.T) {
	legacy := SubmitRequirementsHash([]string{"/acme/a"}, 1, 0, []string{"unit"})
	noGroup := SubmitRequirementsHashWithApprovers([]string{"/acme/a"}, 1, 0, []string{"unit"}, "")
	if legacy != noGroup {
		t.Fatalf("empty approvers hash mismatch: %q != %q", noGroup, legacy)
	}
	withGroup := SubmitRequirementsHashWithApprovers([]string{"/acme/a"}, 1, 0, []string{"unit"}, "@acme/api")
	if withGroup == legacy {
		t.Fatalf("adding approvers group did not change SubmitRequirementsHash: %q", withGroup)
	}
	differentGroup := SubmitRequirementsHashWithApprovers([]string{"/acme/a"}, 1, 0, []string{"unit"}, "@acme/web")
	if differentGroup == withGroup {
		t.Fatalf("changing approvers group did not change hash: %q", differentGroup)
	}
}
