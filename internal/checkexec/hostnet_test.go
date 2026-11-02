package checkexec

import (
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/checks"
)

// On the host network (a Cloudflare runner), a check without network access
// keeps none, one with it gets the host's, and services resolve to
// 127.0.0.1.
func TestContainerRunArgsOnTheHostNetwork(t *testing.T) {
	t.Setenv("GITSLICE_CHECK_HOST_NETWORK", "1")
	argsFor := func(spec checks.CheckSpec, run containerRun) string {
		t.Helper()
		args, err := containerRunArgs("/w", "img", "c1", spec, run)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(args, " ")
	}
	if got := argsFor(checks.CheckSpec{Run: "true"}, containerRun{}); !strings.Contains(got, "--network=none") {
		t.Fatalf("offline check: %s", got)
	}
	if got := argsFor(checks.CheckSpec{Run: "true", Network: true}, containerRun{}); !strings.Contains(got, "--network=host") {
		t.Fatalf("online check: %s", got)
	}
	got := argsFor(checks.CheckSpec{Run: "true"}, containerRun{network: hostNetwork, hosts: []string{"postgres"}})
	if !strings.Contains(got, "--network=host") || !strings.Contains(got, "--add-host postgres:127.0.0.1") {
		t.Fatalf("check with a service: %s", got)
	}
	t.Setenv("GITSLICE_CHECK_HOST_NETWORK", "")
	if got := argsFor(checks.CheckSpec{Run: "true", Network: true}, containerRun{}); strings.Contains(got, "--network") {
		t.Fatalf("online check off the host network: %s", got)
	}
}
