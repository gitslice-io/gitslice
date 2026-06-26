package service

import (
	"net/url"
	"strings"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

type linkRewriteContext struct {
	account   string
	slug      string
	seq       int64
	patchsets []*corev1.Patchset
}

// rewriteAgentFileLinks replaces Markdown inline links whose URL uses the
// gsfile: scheme with web app URLs. Other links and surrounding prose are left
// untouched.
func rewriteAgentFileLinks(text string, rc linkRewriteContext) string {
	if !strings.Contains(text, "gsfile:") {
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
		if !strings.HasPrefix(text[urlStart:], "gsfile:") {
			pos = linkStart + 1
			continue
		}
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

func rewriteAgentFileURL(raw string, rc linkRewriteContext) (string, bool) {
	if rc.account == "" || rc.slug == "" || !strings.HasPrefix(raw, "gsfile:") {
		return "", false
	}
	target := strings.TrimPrefix(raw, "gsfile:")
	relpath, fragment, hasFragment := strings.Cut(target, "#")
	relpath = strings.TrimPrefix(relpath, "./")
	if relpath == "" || strings.HasPrefix(relpath, "/") {
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
	for _, changed := range patchset.GetChangedPaths() {
		if changed == relpath {
			return true
		}
	}
	return false
}
