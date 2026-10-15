package cli

import (
	"context"
	"sort"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *serverShell) completeLine(ctx context.Context, line string) []string {
	tokens := shellLineTokens(line)
	if len(tokens) == 0 {
		return completeShellCommand(line, "")
	}
	if len(tokens) == 1 && !shellLineEndsWithSpace(line) {
		return completeShellCommand(line[:tokens[0].start], tokens[0].value)
	}
	command := tokens[0].value
	argIndex := 1
	token := ""
	prefix := line
	if !shellLineEndsWithSpace(line) {
		current := tokens[len(tokens)-1]
		argIndex = len(tokens) - 1
		token = current.value
		prefix = line[:current.start]
	} else {
		argIndex = len(tokens)
	}
	dirsOnly, ok := shellPathCompletion(command, argIndex)
	if !ok {
		return nil
	}
	completed := s.completePath(ctx, token, dirsOnly)
	out := make([]string, 0, len(completed))
	for _, value := range completed {
		out = append(out, prefix+value)
	}
	return out
}

func completeShellCommand(prefix, token string) []string {
	var out []string
	for _, command := range serverShellCommands {
		if !strings.HasPrefix(command, token) {
			continue
		}
		value := command
		if shellCommandAcceptsPath(command) {
			value += " "
		}
		out = append(out, prefix+value)
	}
	sort.Strings(out)
	return out
}

func shellPathCompletion(command string, argIndex int) (dirsOnly bool, ok bool) {
	switch command {
	case "cd":
		return true, argIndex == 1
	case "ls", "cat", "stat", "mkdir", "touch", "rm":
		return false, argIndex == 1
	case "mv":
		return false, argIndex == 1 || argIndex == 2
	case "write":
		return false, argIndex == 1
	default:
		return false, false
	}
}

func shellCommandAcceptsPath(command string) bool {
	_, ok := shellPathCompletion(command, 1)
	return ok
}

func (s *serverShell) completePath(ctx context.Context, token string, dirsOnly bool) []string {
	dirToken, namePrefix, replacementPrefix := shellCompletionPathParts(token)
	globalDir, err := s.resolve(dirToken)
	if err != nil {
		return nil
	}
	entries, err := s.directoryEntries(ctx, globalDir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if dirsOnly && entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
			continue
		}
		name := s.entryName(entry)
		if name == "" || !strings.HasPrefix(name, namePrefix) {
			continue
		}
		suffix := ""
		if entry.Kind == corev1.EntryKind_ENTRY_KIND_DIRECTORY {
			suffix = "/"
		}
		out = append(out, replacementPrefix+name+suffix)
	}
	sort.Strings(out)
	return compactSortedStrings(out)
}

func shellCompletionPathParts(token string) (dirToken, namePrefix, replacementPrefix string) {
	if token == "" {
		return ".", "", ""
	}
	if strings.HasSuffix(token, "/") {
		return token, "", token
	}
	slash := strings.LastIndex(token, "/")
	if slash < 0 {
		return ".", token, ""
	}
	if slash == 0 {
		return "/", token[1:], "/"
	}
	dirToken = token[:slash]
	replacementPrefix = token[:slash+1]
	return dirToken, token[slash+1:], replacementPrefix
}

type shellLineToken struct {
	value string
	start int
	end   int
}

func shellLineTokens(line string) []shellLineToken {
	var tokens []shellLineToken
	for i := 0; i < len(line); {
		for i < len(line) && isShellLineSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		for i < len(line) && !isShellLineSpace(line[i]) {
			i++
		}
		tokens = append(tokens, shellLineToken{value: line[start:i], start: start, end: i})
	}
	return tokens
}

func shellLineEndsWithSpace(line string) bool {
	return len(line) > 0 && isShellLineSpace(line[len(line)-1])
}

func isShellLineSpace(b byte) bool {
	return b == ' ' || b == '\t'
}

func compactSortedStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value == out[len(out)-1] {
			continue
		}
		out = append(out, value)
	}
	return out
}
