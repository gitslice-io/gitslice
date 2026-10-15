package cli

import (
	"strings"
)

const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiBlue   = "\x1b[34m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
)

func colorize(enabled bool, code, value string) string {
	if !enabled {
		return value
	}
	return code + value + ansiReset
}

func colorUnifiedDiff(diffText string, enabled bool) string {
	if !enabled || diffText == "" {
		return diffText
	}
	lines := strings.SplitAfter(diffText, "\n")
	var b strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSuffix(line, "\n")
		newline := ""
		if strings.HasSuffix(line, "\n") {
			newline = "\n"
		}
		switch {
		case strings.HasPrefix(trimmed, "diff --git") || strings.HasPrefix(trimmed, "index ") || strings.HasPrefix(trimmed, "@@"):
			b.WriteString(colorize(true, ansiDim, trimmed))
		case strings.HasPrefix(trimmed, "+++") || strings.HasPrefix(trimmed, "+"):
			b.WriteString(colorize(true, ansiGreen, trimmed))
		case strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, "-"):
			b.WriteString(colorize(true, ansiRed, trimmed))
		default:
			b.WriteString(trimmed)
		}
		b.WriteString(newline)
	}
	return b.String()
}
