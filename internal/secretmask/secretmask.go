// Package secretmask hides secret values in text that leaves a CI check, such
// as its log and summary (design/33_secrets.md).
package secretmask

import (
	"sort"
	"strings"
)

// Replacement is what a secret value is shown as.
const Replacement = "***"

// MinLength is the shortest value that is masked. Masking shorter ones would
// blank out ordinary words and digits all over a log, and hide nothing worth
// hiding.
const MinLength = 4

// Masker replaces secret values with Replacement. A nil Masker masks nothing.
type Masker struct {
	replacer *strings.Replacer
}

// New builds a Masker for values. A value of several lines is also masked
// line by line, since a log often prints such a value (a key file, say) one
// line at a time.
func New(values []string) *Masker {
	seen := map[string]bool{}
	var hidden []string
	add := func(value string) {
		value = strings.TrimRight(value, "\r")
		if len(strings.TrimSpace(value)) < MinLength || seen[value] {
			return
		}
		seen[value] = true
		hidden = append(hidden, value)
	}
	for _, value := range values {
		add(value)
		if strings.Contains(value, "\n") {
			for _, line := range strings.Split(value, "\n") {
				add(line)
			}
		}
	}
	if len(hidden) == 0 {
		return nil
	}
	// Longest first: a value that contains another is replaced whole.
	sort.Slice(hidden, func(i, j int) bool {
		if len(hidden[i]) != len(hidden[j]) {
			return len(hidden[i]) > len(hidden[j])
		}
		return hidden[i] < hidden[j]
	})
	pairs := make([]string, 0, 2*len(hidden))
	for _, value := range hidden {
		pairs = append(pairs, value, Replacement)
	}
	return &Masker{replacer: strings.NewReplacer(pairs...)}
}

// Mask returns text with every secret value replaced.
func (m *Masker) Mask(text string) string {
	if m == nil || text == "" {
		return text
	}
	return m.replacer.Replace(text)
}
