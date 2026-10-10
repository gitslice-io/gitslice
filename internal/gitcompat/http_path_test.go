package gitcompat

import "testing"

func TestParseGitPathWithAndWithoutSuffix(t *testing.T) {
	for path, want := range map[string][3]string{
		"/git/acme/payment.git/info/refs":   {"acme", "payment", "/acme/payment.git/info/refs"},
		"/git/acme/payment/info/refs":       {"acme", "payment", "/acme/payment.git/info/refs"},
		"/git/acme/payment.git":             {"acme", "payment", "/acme/payment.git"},
		"/git/acme/payment":                 {"acme", "payment", "/acme/payment.git"},
		"/git/acme/payment/git-upload-pack": {"acme", "payment", "/acme/payment.git/git-upload-pack"},
		"/git/acme/my.github.git/info/refs": {"acme", "my.github", "/acme/my.github.git/info/refs"},
		"/git/acme/my.github/HEAD":          {"acme", "my.github", "/acme/my.github.git/HEAD"},
	} {
		account, slice, pathInfo, err := parseGitPath(path)
		if err != nil || account != want[0] || slice != want[1] || pathInfo != want[2] {
			t.Errorf("parseGitPath(%q) = %q, %q, %q, %v; want %v", path, account, slice, pathInfo, err, want)
		}
	}
	for _, path := range []string{"/git/", "/git/acme", "/git/acme/", "/git/acme/.git/info/refs", "/other/acme/payment"} {
		if _, _, _, err := parseGitPath(path); err == nil {
			t.Errorf("parseGitPath(%q) succeeded", path)
		}
	}
}
