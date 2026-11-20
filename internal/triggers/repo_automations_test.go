package triggers

import (
	"os"
	"testing"
)

// TestRepositoryAutomationsFilesParse keeps the repository's own automations
// files valid.
func TestRepositoryAutomationsFilesParse(t *testing.T) {
	for _, p := range []string{"../../.gitslice/automations.yaml", "../../web/.gitslice/automations.yaml"} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		file, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if len(file.Triggers) == 0 {
			t.Fatalf("%s defines no automations", p)
		}
	}
}
