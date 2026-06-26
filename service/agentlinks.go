package service

import (
	"net/url"
	"path"
	"strings"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

type linkRewriteContext struct {
	account        string
	slug           string
	conversationID string
	seq            int64
	patchsets      []*corev1.Patchset
}

// rewriteAgentFileLinks replaces Markdown inline links that point at workspace
// files with web app URLs. Other links and surrounding prose are left untouched.
func rewriteAgentFileLinks(text string, rc linkRewriteContext) string {
	if !textMayContainAgentFileLink(text, rc) {
		return text
	}
	var b strings.Builder
	rewrote := false
	last := 0
	pos := 0
	for pos < len(text) {
		next := strings.IndexByte(text[pos:], '[')
		if next < 0 {
			break
		}
		linkStart := pos + next
		if linkStart > 0 && text[linkStart-1] == '!' {
			pos = linkStart + 1
			continue
		}
		labelEndRel := strings.IndexByte(text[linkStart+1:], ']')
		if labelEndRel < 0 {
			break
		}
		labelEnd := linkStart + 1 + labelEndRel
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
		rewritten, ok := rewriteAgentFileURL(text[urlStart:urlEnd], rc)
		if ok {
			if !rewrote {
				b.Grow(len(text))
				rewrote = true
			}
			b.WriteString(text[last:urlStart])
			b.WriteString(rewritten)
			last = urlEnd
		}
		pos = urlEnd + 1
	}
	if !rewrote {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

func textMayContainAgentFileLink(text string, rc linkRewriteContext) bool {
	return strings.Contains(text, "gsfile:") ||
		(rc.conversationID != "" && strings.Contains(text, "/conversations/"+rc.conversationID+"/"))
}

func rewriteAgentFileURL(raw string, rc linkRewriteContext) (string, bool) {
	if rc.account == "" || rc.slug == "" {
		return "", false
	}
	relpath, fragment, hasFragment, ok := agentFileRelpath(raw, rc)
	if !ok {
		return "", false
	}

	u := url.URL{}
	if owner := owningPatchset(rc.patchsets, rc.seq); owner != nil && patchsetChangedPath(owner, relpath) {
		u.Path = "/cs/" + owner.GetChangesetId()
		u.RawQuery = "to=" + url.QueryEscape(owner.GetId()) + "&file=" + url.QueryEscape(relpath)
	} else {
		u.Path = "/slices/" + rc.account + "/" + rc.slug
		u.RawQuery = "path=" + url.QueryEscape(relpath)
	}
	if hasFragment {
		u.Fragment = fragment
	}
	return u.String(), true
}

func agentFileRelpath(raw string, rc linkRewriteContext) (relpath string, fragment string, hasFragment bool, ok bool) {
	target, fragment, hasFragment := strings.Cut(raw, "#")
	switch {
	case strings.HasPrefix(target, "gsfile:"):
		relpath = strings.TrimPrefix(target, "gsfile:")
	default:
		var pathOK bool
		relpath, pathOK = conversationWorkspaceRelpath(target, rc.conversationID)
		if !pathOK {
			return "", "", false, false
		}
	}
	relpath, ok = cleanAgentFileRelpath(relpath)
	return relpath, fragment, hasFragment, ok
}

func conversationWorkspaceRelpath(raw, conversationID string) (string, bool) {
	if conversationID == "" {
		return "", false
	}
	candidate := raw
	if strings.HasPrefix(candidate, "file://") {
		u, err := url.Parse(candidate)
		if err != nil || u.Path == "" {
			return "", false
		}
		candidate = u.Path
	} else if unescaped, err := url.PathUnescape(candidate); err == nil {
		candidate = unescaped
	}
	marker := "/conversations/" + conversationID + "/"
	idx := strings.Index(candidate, marker)
	if idx < 0 {
		return "", false
	}
	relpath := candidate[idx+len(marker):]
	if relpath == "" {
		return "", false
	}
	return relpath, true
}

func cleanAgentFileRelpath(relpath string) (string, bool) {
	relpath = strings.TrimPrefix(relpath, "./")
	relpath = path.Clean(relpath)
	if relpath == "." || relpath == "" || strings.HasPrefix(relpath, "/") || relpath == ".." || strings.HasPrefix(relpath, "../") {
		return "", false
	}
	return relpath, true
}

func owningPatchset(patchsets []*corev1.Patchset, seq int64) *corev1.Patchset {
	var owner *corev1.Patchset
	var ownerSeq int64
	for _, patchset := range patchsets {
		if patchset == nil {
			continue
		}
		patchsetSeq := patchset.GetAuthoringConversationSeq()
		if patchsetSeq < seq {
			continue
		}
		if owner == nil || patchsetSeq < ownerSeq {
			owner = patchset
			ownerSeq = patchsetSeq
		}
	}
	return owner
}

func patchsetChangedPath(patchset *corev1.Patchset, relpath string) bool {
	want := comparableAgentFilePath(relpath)
	if want == "" {
		return false
	}
	for _, changed := range patchset.GetChangedPaths() {
		if comparableAgentFilePath(changed) == want {
			return true
		}
	}
	return false
}

func comparableAgentFilePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	p = path.Clean(p)
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}
