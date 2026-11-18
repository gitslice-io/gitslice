package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func testHumanSubject(id string) Subject {
	return Subject{ID: id, Kind: "user", ControllerSubjectID: id}
}

func testAgentSubject(id, controllerID string) Subject {
	return Subject{ID: id, Kind: "agent", ControllerSubjectID: controllerID}
}

func TestEvaluateSubmitRequirementsApprovals(t *testing.T) {
	alice := testHumanSubject("user_alice")
	bob := testHumanSubject("user_bob")
	carol := testHumanSubject("user_carol")

	req := &corev1.SubmitRequirements{RequiredApprovals: 2}
	reason := EvaluateSubmitRequirements(req, []Subject{alice}, []Subject{alice, bob, bob}, nil)
	if !strings.Contains(reason, "requires 2") || !strings.Contains(reason, "has 1") {
		t.Fatalf("reason = %q, want missing approval count", reason)
	}

	reason = EvaluateSubmitRequirements(req, []Subject{alice}, []Subject{bob, carol}, nil)
	if reason != "" {
		t.Fatalf("reason = %q, want satisfied", reason)
	}
}

func TestEvaluateSubmitRequirementsControllersAndHumanApprovals(t *testing.T) {
	alice := testHumanSubject("user_alice")
	bob := testHumanSubject("user_bob")
	carol := testHumanSubject("user_carol")
	aliceAgent1 := testAgentSubject("agent_alice_1", "user_alice")
	aliceAgent2 := testAgentSubject("agent_alice_2", "user_alice")
	bobAgent1 := testAgentSubject("agent_bob_1", "user_bob")
	bobAgent2 := testAgentSubject("agent_bob_2", "user_bob")
	unclaimed1 := testAgentSubject("agent_unclaimed_1", "")
	unclaimed2 := testAgentSubject("agent_unclaimed_2", "")

	// One person's agents cannot approve each other, nor can the controller approve their own agent.
	req1 := &corev1.SubmitRequirements{RequiredApprovals: 1}
	if reason := EvaluateSubmitRequirements(req1, []Subject{aliceAgent1}, []Subject{aliceAgent2, alice}, nil); !strings.Contains(reason, "has 0") {
		t.Fatalf("same-controller approvals reason = %q, want has 0", reason)
	}
	if reason := EvaluateSubmitRequirements(req1, []Subject{alice}, []Subject{aliceAgent1}, nil); !strings.Contains(reason, "has 0") {
		t.Fatalf("agent approving controller reason = %q, want has 0", reason)
	}

	// Unclaimed agents never count, even on someone else's changeset or when two unclaimed agents exist.
	if reason := EvaluateSubmitRequirements(req1, []Subject{alice}, []Subject{unclaimed1, unclaimed2}, nil); !strings.Contains(reason, "has 0") {
		t.Fatalf("unclaimed approvers reason = %q, want has 0", reason)
	}
	if reason := EvaluateSubmitRequirements(req1, []Subject{unclaimed1}, []Subject{unclaimed1, unclaimed2}, nil); !strings.Contains(reason, "has 0") {
		t.Fatalf("unclaimed author + unclaimed approver reason = %q, want has 0", reason)
	}

	// An unclaimed author's own subject ID stays excluded even when somehow paired with a controller in tests.
	filtered := FilterApprovalSubjects([]Subject{{ID: unclaimed1.ID, Kind: "agent", ControllerSubjectID: "user_bob"}}, unclaimed1)
	if len(filtered) != 0 {
		t.Fatalf("FilterApprovalSubjects did not exclude author's own subject_id: %#v", filtered)
	}

	// Two agents with the same controller count as 1 distinct controller approval, not 2.
	req2 := &corev1.SubmitRequirements{RequiredApprovals: 2}
	if reason := EvaluateSubmitRequirements(req2, []Subject{alice}, []Subject{bobAgent1, bobAgent2, bob}, nil); !strings.Contains(reason, "requires 2") || !strings.Contains(reason, "has 1") {
		t.Fatalf("same-controller agent pair reason = %q, want has 1", reason)
	}
	if reason := EvaluateSubmitRequirements(req2, []Subject{alice}, []Subject{bobAgent1, carol}, nil); reason != "" {
		t.Fatalf("distinct controllers reason = %q, want satisfied", reason)
	}

	// Non-mechanical patchset author excludes their controller as well.
	if reason := EvaluateSubmitRequirements(req1, []Subject{alice, bobAgent1}, []Subject{bob, bobAgent2}, nil); !strings.Contains(reason, "has 0") {
		t.Fatalf("patchset author controller approval reason = %q, want has 0", reason)
	}

	// RequiredHumanApprovals requires distinct non-excluded approvers with kind == "user".
	humanReq := &corev1.SubmitRequirements{RequiredApprovals: 1, RequiredHumanApprovals: 1}
	reason := EvaluateSubmitRequirements(humanReq, []Subject{alice}, []Subject{bobAgent1}, nil)
	want := "required human approvals not satisfied: requires 1 human approval(s), has 0"
	if reason != want {
		t.Fatalf("human approval block reason = %q, want %q", reason, want)
	}
	if !IsRequirementBlock(reason) {
		t.Fatalf("IsRequirementBlock(%q) = false, want true", reason)
	}
	// Author's own human approval cannot satisfy RequiredHumanApprovals.
	if reason := EvaluateSubmitRequirements(humanReq, []Subject{aliceAgent1}, []Subject{alice, bobAgent1}, nil); reason != want {
		t.Fatalf("controller self-human-approval reason = %q, want %q", reason, want)
	}
	// A different human satisfies both RequiredApprovals and RequiredHumanApprovals.
	if reason := EvaluateSubmitRequirements(humanReq, []Subject{aliceAgent1}, []Subject{bob}, nil); reason != "" {
		t.Fatalf("other human approval reason = %q, want satisfied", reason)
	}
}

func TestEvaluateSubmitRequirementsChecks(t *testing.T) {
	alice := testHumanSubject("user_alice")
	req := &corev1.SubmitRequirements{RequiredChecks: []string{"unit", "integration"}}
	reason := EvaluateSubmitRequirements(req, []Subject{alice}, nil, map[string]string{"unit": CheckStatusPass})
	if !strings.Contains(reason, `required check "integration" has no result`) {
		t.Fatalf("reason = %q, want missing check", reason)
	}

	reason = EvaluateSubmitRequirements(req, []Subject{alice}, nil, map[string]string{
		"unit":        CheckStatusPass,
		"integration": CheckStatusFail,
	})
	if !strings.Contains(reason, `required check "integration" is failing`) {
		t.Fatalf("reason = %q, want failing check", reason)
	}

	reason = EvaluateSubmitRequirements(req, []Subject{alice}, nil, map[string]string{
		"unit":        CheckStatusPass,
		"integration": CheckStatusPass,
	})
	if reason != "" {
		t.Fatalf("reason = %q, want satisfied", reason)
	}
}

func TestSubmitRequirementsHashCanonicalizesInputs(t *testing.T) {
	included := []string{"/acme/payment", "/acme"}
	checks := []string{" integration ", "", "unit"}

	got := SubmitRequirementsHash(included, 2, 0, checks)
	reordered := SubmitRequirementsHash([]string{"/acme", "/acme/payment"}, 2, 0, []string{"unit", "integration"})
	if got != reordered {
		t.Fatalf("hash changed after reordering/normalizing inputs:\n got %q\nwant %q", got, reordered)
	}
	// Zero required_human_approvals must keep the legacy hash identical.
	legacyPayload, _ := json.Marshal(struct {
		IncludedPaths     []string `json:"included_paths"`
		RequiredApprovals int32    `json:"required_approvals"`
		RequiredChecks    []string `json:"required_checks"`
	}{
		IncludedPaths:     []string{"/acme", "/acme/payment"},
		RequiredApprovals: 2,
		RequiredChecks:    []string{"integration", "unit"},
	})
	legacySum := sha256.Sum256(legacyPayload)
	if wantLegacy := "submitreq_sha256:" + hex.EncodeToString(legacySum[:]); got != wantLegacy {
		t.Fatalf("zero required_human_approvals hash = %q, want legacy %q", got, wantLegacy)
	}
	if got == SubmitRequirementsHash([]string{"/acme", "/acme/payment"}, 1, 0, []string{"unit", "integration"}) {
		t.Fatalf("hash did not change when required approvals changed")
	}
	if got == SubmitRequirementsHash([]string{"/acme", "/acme/payment"}, 2, 1, []string{"unit", "integration"}) {
		t.Fatalf("hash did not change when required human approvals changed")
	}
	if included[0] != "/acme/payment" || checks[0] != " integration " || checks[1] != "" {
		t.Fatalf("SubmitRequirementsHash mutated caller inputs: included=%#v checks=%#v", included, checks)
	}
}

func TestSubmitBlockedErrorAndCodeClassification(t *testing.T) {
	cases := []struct {
		code         SubmitBlockCode
		reason       string
		wantCode     SubmitBlockCode
		wantReqBlock bool
		wantCategory string
	}{
		{
			code:         SubmitBlockCodeStalePathBase,
			reason:       StaleBaseReason,
			wantCode:     SubmitBlockCodeStalePathBase,
			wantReqBlock: false,
			wantCategory: SubmitReasonStalePathBase,
		},
		{
			code:         SubmitBlockCodeNone,
			reason:       "changed path /acme/foo is outside latest slice definition, refresh the changeset",
			wantCode:     SubmitBlockCodeRequirementsChanged,
			wantReqBlock: false,
			wantCategory: SubmitReasonRequirementsChanged,
		},
		{
			code:         SubmitBlockCodeNone,
			reason:       "required approvals not satisfied: requires 1 distinct non-author approval(s), has 0",
			wantCode:     SubmitBlockCodeApprovalsMissing,
			wantReqBlock: true,
			wantCategory: SubmitReasonApprovalsMissing,
		},
		{
			code:         SubmitBlockCodeNone,
			reason:       "path owner approvals not satisfied for /acme/v2/api.go",
			wantCode:     SubmitBlockCodeApprovalsMissing,
			wantReqBlock: true,
			wantCategory: SubmitReasonApprovalsMissing,
		},
		{
			code:         SubmitBlockCodeNone,
			reason:       ".gitslice configuration path /acme/.gitslice/checks.yaml requires approval",
			wantCode:     SubmitBlockCodeApprovalsMissing,
			wantReqBlock: true,
			wantCategory: SubmitReasonApprovalsMissing,
		},
		{
			code:         SubmitBlockCodeNone,
			reason:       `required check "unit" has no result for current patchset`,
			wantCode:     SubmitBlockCodeChecksMissing,
			wantReqBlock: true,
			wantCategory: SubmitReasonChecksMissing,
		},
		{
			code:         SubmitBlockCodeNone,
			reason:       "BlockedOnBaseChangeset",
			wantCode:     SubmitBlockCodeBlockedOnParent,
			wantReqBlock: false,
			wantCategory: SubmitReasonConflict,
		},
	}

	for _, tc := range cases {
		err := NewSubmitBlockedError(tc.code, tc.reason)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("NewSubmitBlockedError(%q, %q) does not unwrap to ErrConflict", tc.code, tc.reason)
		}
		gotCode, ok := SubmitBlockCodeOf(err)
		if !ok || gotCode != tc.wantCode {
			t.Fatalf("SubmitBlockCodeOf(%v) = (%q, %v), want (%q, true)", err, gotCode, ok, tc.wantCode)
		}
		if gotCode.IsRequirementBlock() != tc.wantReqBlock || IsRequirementBlock(tc.reason) != tc.wantReqBlock {
			t.Fatalf("IsRequirementBlock(%q) = %v, want %v", tc.reason, gotCode.IsRequirementBlock(), tc.wantReqBlock)
		}
		if gotCode.Category() != tc.wantCategory || SubmitBlockedReasonCategory(tc.reason) != tc.wantCategory {
			t.Fatalf("Category(%q) = %q, want %q", tc.reason, gotCode.Category(), tc.wantCategory)
		}
	}
}
