//go:build linux || darwin

package cli

import (
	"strings"
	"testing"
)

func TestMountTimeoutHintNamesTheMacFUSEApprovalSteps(t *testing.T) {
	mac := mountTimeoutHint("darwin")
	for _, want := range []string{"macFUSE", "Privacy & Security", "Allow", "restart", "gs doctor", "gs init"} {
		if !strings.Contains(mac, want) {
			t.Fatalf("macOS hint %q is missing %q", mac, want)
		}
	}
	linux := mountTimeoutHint("linux")
	if strings.Contains(linux, "macFUSE") || !strings.Contains(linux, "/dev/fuse") {
		t.Fatalf("Linux hint %q", linux)
	}
}
