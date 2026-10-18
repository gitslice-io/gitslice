package cli

import (
	"os"
	"strings"
)

// isMounted reports whether a file system is mounted at dir. It reads the
// mount table, never dir itself, which hangs when its daemon is gone.
func isMounted(dir string) bool {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && unescapeMountPath(fields[4]) == dir {
			return true
		}
	}
	return false
}

func unescapeMountPath(p string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(p)
}
