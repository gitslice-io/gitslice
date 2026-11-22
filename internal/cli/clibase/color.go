package clibase

import (
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	AnsiReset  = "\x1b[0m"
	AnsiDim    = "\x1b[2m"
	AnsiBlue   = "\x1b[34m"
	AnsiCyan   = "\x1b[36m"
	AnsiGreen  = "\x1b[32m"
	AnsiRed    = "\x1b[31m"
	AnsiYellow = "\x1b[33m"
)

func IsTerminal(w any) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func Colorize(enabled bool, code, value string) string {
	if !enabled {
		return value
	}
	return code + value + AnsiReset
}

func ColorUnifiedDiff(diffText string, enabled bool) string {
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
			b.WriteString(Colorize(true, AnsiDim, trimmed))
		case strings.HasPrefix(trimmed, "+++") || strings.HasPrefix(trimmed, "+"):
			b.WriteString(Colorize(true, AnsiGreen, trimmed))
		case strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, "-"):
			b.WriteString(Colorize(true, AnsiRed, trimmed))
		default:
			b.WriteString(trimmed)
		}
		b.WriteString(newline)
	}
	return b.String()
}
