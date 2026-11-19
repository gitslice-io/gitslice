package storage

import (
	"errors"
	"slices"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestEvaluateControllerApprovalsOwnerAndGroupModes(t *testing.T) {
	alice := testHumanSubject("user_alice")
	aliceAgent := testAgentSubject("agent_alice", "user_alice")
	bob := testHumanSubject("user_bob")
	bobAgent := testAgentSubject("agent_bob", "user_bob")
	unclaimed := testAgentSubject("agent_unclaimed", "")

	// Self-author by controller (OWNERS mode): alice's agent is listed as owner, alice authors -> counts 1.
	got := EvaluateControllerApprovals(ControllerApprovalInput{
		Authors:                 []Subject{alice},
		AllowedSubjects:         []Subject{aliceAgent, unclaimed},
		AllowedSubjectIDs:       map[string]bool{"agent_alice": true, "agent_unclaimed": true},
		RestrictToAllowed:       true,
		CountSelfAuthorApproval: true,
		SelfOwnBySubject:        false,
	})
	if got.ControllerCount != 1 {
		t.Fatalf("OWNERS controller self-approval count = %d, want 1", got.ControllerCount)
	}

	// Self-author by exact subject (.gitslice mode): alice's agent is in AllowedSubjectIDs, alice is not -> 0.
	got = EvaluateControllerApprovals(ControllerApprovalInput{
		Authors:                 []Subject{alice},
		AllowedSubjects:         []Subject{aliceAgent},
		AllowedSubjectIDs:       map[string]bool{"agent_alice": true},
		RestrictToAllowed:       true,
		CountSelfAuthorApproval: true,
		SelfOwnBySubject:        true,
	})
	if got.ControllerCount != 0 {
		t.Fatalf(".gitslice subject self-approval count = %d, want 0", got.ControllerCount)
	}

	// .gitslice mode: non-admin agent authors, human admin owner (same controller) explicitly approves -> 1.
	got = EvaluateControllerApprovals(ControllerApprovalInput{
		Authors:                 []Subject{aliceAgent},
		Approvers:               []Subject{alice},
		AllowedSubjects:         []Subject{alice},
		AllowedSubjectIDs:       map[string]bool{"user_alice": true},
		RestrictToAllowed:       true,
		CountSelfAuthorApproval: true,
		SelfOwnBySubject:        true,
	})
	if got.ControllerCount != 1 || got.HumanCount != 1 {
		t.Fatalf(".gitslice human owner approving own agent = %+v, want ControllerCount=1 HumanCount=1", got)
	}

	// .gitslice mode: sibling agent under same controller cannot approve on the human owner's behalf, even if allowed.
	aliceAgent2 := testAgentSubject("agent_alice_2", "user_alice")
	got = EvaluateControllerApprovals(ControllerApprovalInput{
		Authors:                 []Subject{aliceAgent},
		Approvers:               []Subject{aliceAgent2},
		AllowedSubjects:         []Subject{alice, aliceAgent2},
		AllowedSubjectIDs:       map[string]bool{"user_alice": true, "agent_alice_2": true},
		RestrictToAllowed:       true,
		CountSelfAuthorApproval: true,
		SelfOwnBySubject:        true,
	})
	if got.ControllerCount != 0 || got.HumanCount != 0 {
		t.Fatalf(".gitslice sibling agent approving same-controller agent = %+v, want ControllerCount=0 HumanCount=0", got)
	}

	// .gitslice mode: human admin authoring directly AND approving never double-counts.
	got = EvaluateControllerApprovals(ControllerApprovalInput{
		Authors:                 []Subject{alice},
		Approvers:               []Subject{alice},
		AllowedSubjects:         []Subject{alice},
		AllowedSubjectIDs:       map[string]bool{"user_alice": true},
		RestrictToAllowed:       true,
		CountSelfAuthorApproval: true,
		SelfOwnBySubject:        true,
	})
	if got.ControllerCount != 1 || got.HumanCount != 1 {
		t.Fatalf(".gitslice human admin self-author + approve = %+v, want ControllerCount=1 HumanCount=1", got)
	}

	// Unclaimed co-author blocks self-approval credit, requiring an external owner controller.
	got = EvaluateControllerApprovals(ControllerApprovalInput{
		Authors:                 []Subject{alice, unclaimed},
		Approvers:               []Subject{bob, bobAgent},
		AllowedSubjects:         []Subject{alice, bob, bobAgent},
		AllowedSubjectIDs:       map[string]bool{"user_alice": true, "user_bob": true, "agent_bob": true},
		RestrictToAllowed:       true,
		CountSelfAuthorApproval: true,
		SelfOwnBySubject:        false,
	})
	if got.ControllerCount != 1 || got.HumanCount != 1 {
		t.Fatalf("co-authored with unclaimed agent = %+v, want ControllerCount=1 HumanCount=1", got)
	}
}

func TestValidateSubmitPreconditionsAndDomainTransitions(t *testing.T) {
	code, reason, err := ValidateSubmitPreconditions(SubmitPreconditionInput{
		Status:            "draft",
		CurrentPatchsetID: "ps_1",
		HasConflicts:      true,
	})
	if !errors.Is(err, ErrConflict) || code != SubmitBlockCodeNone || reason != "" {
		t.Fatalf("conflicts result = (%q, %q, %v), want ErrConflict", code, reason, err)
	}

	code, reason, err = ValidateSubmitPreconditions(SubmitPreconditionInput{
		Status:            "draft",
		CurrentPatchsetID: "ps_1",
		HasParent:         true,
		ParentStatus:      "draft",
	})
	if err != nil || code != SubmitBlockCodeBlockedOnParent || reason != "BlockedOnBaseChangeset" {
		t.Fatalf("parent draft result = (%q, %q, %v), want blocked_on_parent", code, reason, err)
	}

	code, reason, err = ValidateSubmitPreconditions(SubmitPreconditionInput{
		Status:            "draft",
		CurrentPatchsetID: "ps_1",
		ChangedPaths:      []string{"/acme/other/file.go"},
		IncludedPaths:     []string{"/acme/payment"},
	})
	if err != nil || code != SubmitBlockCodeRequirementsChanged || reason == "" {
		t.Fatalf("outside slice result = (%q, %q, %v), want requirements_changed", code, reason, err)
	}

	fileHead := PathHeadFromFile(FileEntry{Path: "/acme/a.go", BlobID: "b1", ContentHash: "h1", Mode: 0o100644})
	if err := ValidateAcceptedPathBase(fileHead, &corev1.PathBase{
		Path:             "/acme/a.go",
		Exists:           true,
		EntryFingerprint: fileHead.EntryFingerprint,
	}); err != nil {
		t.Fatalf("matching PathBase err = %v", err)
	}
	if err := ValidateAcceptedPathBase(fileHead, &corev1.PathBase{
		Path:             "/acme/a.go",
		Exists:           false,
		EntryFingerprint: MissingEntryFingerprint(),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale missing PathBase err = %v, want ErrConflict", err)
	}

	if _, err := ResolveChangesetSelectorMatches("cs_abcd", true, false, []string{"cs_abcd1111", "cs_abcd2222"}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ambiguous selector err = %v, want ErrInvalid", err)
	}

	collabs, added := ApplyCollaboratorAddition("user_alice", []string{"user_bob"}, "user_alice")
	if added || !slices.Equal(collabs, []string{"user_bob"}) {
		t.Fatalf("author added as collaborator: %v, %v", collabs, added)
	}
	collabs, added = ApplyCollaboratorAddition("user_alice", collabs, "user_carol")
	if !added || !slices.Equal(collabs, []string{"user_bob", "user_carol"}) {
		t.Fatalf("collaborator addition = %v, %v", collabs, added)
	}
	author, orig, collabs, changed := ApplyHandoff("user_alice", "", collabs, "user_carol")
	if !changed || author != "user_carol" || orig != "user_alice" || !slices.Equal(collabs, []string{"user_bob"}) {
		t.Fatalf("ApplyHandoff = (%q, %q, %v, %v)", author, orig, collabs, changed)
	}
	if role, err := ValidateAccountMemberRole(" ADMIN "); err != nil || role != "admin" {
		t.Fatalf("ValidateAccountMemberRole = (%q, %v)", role, err)
	}
	if _, err := ValidateAccountMemberRole("superadmin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid role err = %v, want ErrInvalid", err)
	}
}
