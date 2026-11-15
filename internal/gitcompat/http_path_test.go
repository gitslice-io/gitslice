package gitcompat

import "testing"

func TestParseGitPathWithAndWithoutSuffix(t *testing.T) {
	for path, want := range map[string][3]string{
		"/git/acme/payment.git/info/refs":        {"acme", "payment", "/info/refs"},
		"/git/acme/payment/info/refs":            {"acme", "payment", "/info/refs"},
		"/git/acme/payment/git-upload-pack":      {"acme", "payment", "/git-upload-pack"},
		"/git/acme/payment.git/git-receive-pack": {"acme", "payment", "/git-receive-pack"},
		"/git/acme/my.github.git/info/refs":      {"acme", "my.github", "/info/refs"},
		"/git/acme/my.github/HEAD":               {"acme", "my.github", "/HEAD"},
	} {
		account, slice, suffix, err := parseGitPath(path)
		if err != nil || account != want[0] || slice != want[1] || suffix != want[2] {
			t.Errorf("parseGitPath(%q) = %q, %q, %q, %v; want %v", path, account, slice, suffix, err, want)
		}
	}
	for _, path := range []string{
		"/git/",
		"/git/acme",
		"/git/acme/",
		"/git/acme/payment",
		"/git/acme/payment.git",
		"/git/acme/.git/info/refs",
		"/git/acme/payment.git.git/info/refs",
		"/git/../acme/payment.git/info/refs",
		"/git/acme/../payment.git/info/refs",
		"/git/acme/payment.git/../other.git/info/refs",
		"/git/.mirrors/payment.git/info/refs",
		"/git/_internal/compact/info/refs",
		"/git/-bad/payment.git/info/refs",
		"/git/acme/payment.git/objects/info/packs",
		"/git/acme/payment.git/config",
		"/git/acme//payment.git/info/refs",
		"/git/acme/payment.git/info/refs/",
		"/other/acme/payment",
	} {
		if _, _, _, err := parseGitPath(path); err == nil {
			t.Errorf("parseGitPath(%q) succeeded", path)
		}
	}
}
