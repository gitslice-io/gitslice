package storage

import (
	"strings"
	"testing"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestEvaluateSubmitRequirementsApprovals(t *testing.T) {
	req := &corev1.SubmitRequirements{RequiredApprovals: 2}
	reason := EvaluateSubmitRequirements(req, "user_alice", []string{"user_alice", "user_bob", "user_bob"}, nil)
	if !strings.Contains(reason, "requires 2") || !strings.Contains(reason, "has 1") {
		t.Fatalf("reason = %q, want missing approval count", reason)
	}

	reason = EvaluateSubmitRequirements(req, "user_alice", []string{"user_bob", "ci_bot"}, nil)
	if reason != "" {
		t.Fatalf("reason = %q, want satisfied", reason)
	}
}

func TestEvaluateSubmitRequirementsChecks(t *testing.T) {
	req := &corev1.SubmitRequirements{RequiredChecks: []string{"unit", "integration"}}
	reason := EvaluateSubmitRequirements(req, "user_alice", nil, map[string]string{"unit": CheckStatusPass})
	if !strings.Contains(reason, `required check "integration" has no result`) {
		t.Fatalf("reason = %q, want missing check", reason)
	}

	reason = EvaluateSubmitRequirements(req, "user_alice", nil, map[string]string{
		"unit":        CheckStatusPass,
		"integration": CheckStatusFail,
	})
	if !strings.Contains(reason, `required check "integration" is failing`) {
		t.Fatalf("reason = %q, want failing check", reason)
	}

	reason = EvaluateSubmitRequirements(req, "user_alice", nil, map[string]string{
		"unit":        CheckStatusPass,
		"integration": CheckStatusPass,
	})
	if reason != "" {
		t.Fatalf("reason = %q, want satisfied", reason)
	}
}
