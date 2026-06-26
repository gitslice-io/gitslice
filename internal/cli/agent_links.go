package cli

import (
	"net/url"
	"path/filepath"
	"strings"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func normalizeAgentWorkspaceEventLinks(event *corev1.AgentEvent, workdir string) {
	if event == nil || event.GetText() == "" {
		return
	}
	if strings.ToLower(event.GetRole()) != "agent" {
		return
	}
	switch strings.ToLower(event.GetType()) {
	case "message", "message_delta":
	default:
		return
	}
	event.Text = rewriteAgentWorkspaceMarkdownLinks(event.GetText(), workdir)
}

func rewriteAgentWorkspaceMarkdownLinks(text, workdir string) string {
	root, ok := agentWorkspaceRoot(workdir)
	if !ok {
		return text
	}

	var b strings.Builder
	rewrote := false
	last := 0
	for pos := 0; pos < len(text); {
		linkStartRel := strings.IndexByte(text[pos:], '[')
		if linkStartRel < 0 {
			break
		}
		linkStart := pos + linkStartRel
		if linkStart > 0 && text[linkStart-1] == '!' {
			pos = linkStart + 1
			continue
		}
		labelEndRel := strings.IndexByte(text[linkStart:], ']')
		if labelEndRel < 0 {
			break
		}
		labelEnd := linkStart + labelEndRel
		if labelEnd+1 >= len(text) || text[labelEnd+1] != '(' {
			pos = linkStart + 1
			continue
		}
		urlStart := labelEnd + 2
		urlEndRel := strings.IndexByte(text[urlStart:], ')')
		if urlEndRel < 0 {
			break
		}
		urlEnd := urlStart + urlEndRel
		replacement, ok := rewriteAgentWorkspaceLinkTarget(text[urlStart:urlEnd], root)
		if !ok {
			pos = linkStart + 1
			continue
		}
		if !rewrote {
			b.Grow(len(text))
			rewrote = true
		}
		b.WriteString(text[last:urlStart])
		b.WriteString(replacement)
		last = urlEnd
		pos = urlEnd + 1
	}
	if !rewrote {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

func agentWorkspaceRoot(workdir string) (string, bool) {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return "", false
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return "", false
	}
	return filepath.Clean(abs), true
}

func rewriteAgentWorkspaceLinkTarget(raw, root string) (string, bool) {
	target := strings.TrimSpace(raw)
	if strings.HasPrefix(target, "<") && strings.HasSuffix(target, ">") {
		target = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">"))
	}
	target, fragment, hasFragment := strings.Cut(target, "#")
	target = strings.TrimSpace(target)
	candidate, ok := agentWorkspaceLinkPath(target)
	if !ok {
		return "", false
	}
	relpath, ok := agentWorkspaceRelpath(root, candidate)
	if !ok {
		return "", false
	}
	out := "gsfile:" + relpath
	if hasFragment {
		out += "#" + fragment
	}
	return out, true
}

func agentWorkspaceLinkPath(target string) (string, bool) {
	if target == "" || strings.HasPrefix(target, "gsfile:") {
		return "", false
	}
	if strings.HasPrefix(target, "file://") {
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "file" || u.Path == "" {
			return "", false
		}
		if u.Host != "" && u.Host != "localhost" {
			return "", false
		}
		target = u.Path
	}
	if unescaped, err := url.PathUnescape(target); err == nil {
		target = unescaped
	}
	return target, true
}

func agentWorkspaceRelpath(root, candidate string) (string, bool) {
	if !filepath.IsAbs(candidate) {
		return "", false
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, filepath.Clean(abs))
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}
