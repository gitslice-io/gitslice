package checks

import (
	"os"
	"testing"
)

// TestRepositoryChecksFilesParse keeps the repository's own checks files
// (used when the Gitslice source is hosted on Gitslice) valid.
func TestRepositoryChecksFilesParse(t *testing.T) {
	for _, path := range []string{"../../.gitslice/checks.yaml", "../../web/.gitslice/checks.yaml"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(file.Checks) == 0 {
			t.Fatalf("%s defines no checks", path)
		}
	}
}
