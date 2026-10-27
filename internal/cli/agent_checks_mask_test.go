package cli

import (
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestCheckSecretMaskerHidesOnlySecretEnv(t *testing.T) {
	spec := &corev1.CheckRunSpec{
		Env:       map[string]string{"CI_TOKEN": "super-secret", "PLAIN_ENV": "plainvalue"},
		SecretEnv: []string{"CI_TOKEN", "MISSING"},
	}
	got := checkSecretMasker(spec).Mask("super-secret plainvalue")
	if got != "*** plainvalue" {
		t.Fatalf("masked = %q, want only the secret hidden", got)
	}
	if got := checkSecretMasker(&corev1.CheckRunSpec{}).Mask("plain"); got != "plain" {
		t.Fatalf("masked without secrets = %q", got)
	}
}
