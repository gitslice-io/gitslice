// Package markdown renders a repository's Markdown (a folder's README) to HTML
// that is safe to put in a page as it is, on the server, so a slice's page can
// be served with its README already in it.
//
// The pipeline mirrors what the web viewer does in the browser (marked, then
// DOMPurify, then marking repository images): GitHub-flavored Markdown with
// raw HTML allowed, two rewrites on the parsed tree, then a sanitizer that
// keeps only an allowlist of elements, attributes and URL schemes. The
// sanitizer runs last, so nothing after it can reintroduce markup. An
// image whose src is a path in the repository loses its src and gets the path
// in data-repo-image, for the viewer to load through the API (the page's own
// address does not serve it); a <picture> source with such a srcset gets it in
// data-repo-srcset, for the viewer to resolve with the site's theme. Every link
// opens in a new tab with no referrer, as the viewer's sanitizer hook does.
package markdown

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Options says where the document is, to resolve the images it refers to.
type Options struct {
	// Directory is the repository directory holding the document.
	Directory string
	// Root is the project root that root-absolute image paths ("/docs/x.png")
	// are relative to (the slice's included path holding the document); empty
	// leaves such paths alone.
	Root string
}

var converter = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	// READMEs use raw HTML (a centered title, a <picture> logo, <details>);
	// it is allowed here and made safe by the sanitizer.
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

var policy = newPolicy()

func newPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.RequireNoFollowOnLinks(false)
	p.AllowDataURIImages()
	p.AllowElements("picture", "details", "summary", "source")
	p.AllowAttrs("srcset", "media", "type").OnElements("source")
	p.AllowAttrs("srcset").OnElements("img")
	p.AllowAttrs("align").OnElements("p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "img", "table", "td", "th", "tr")
	p.AllowAttrs("open").OnElements("details")
	// Task lists.
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("checked", "disabled").OnElements("input")
	p.AllowElements("input")
	// Code blocks keep their language, for highlighting and diagrams.
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^language-[\w+#.-]+$`)).OnElements("code")
	// What the rewrites add, and only in the form they add it.
	repoPath := regexp.MustCompile(`^/[^"'<>\s]*$`)
	p.AllowAttrs("data-repo-image").Matching(repoPath).OnElements("img")
	p.AllowAttrs("data-repo-srcset").Matching(repoPath).OnElements("source")
	p.AllowAttrs("target").Matching(regexp.MustCompile(`^_blank$`)).OnElements("a")
	p.AllowAttrs("rel").Matching(regexp.MustCompile(`^noopener noreferrer$`)).OnElements("a")
	return p
}

// Render turns Markdown into safe HTML.
func Render(source []byte, opts Options) (string, error) {
	var out bytes.Buffer
	if err := converter.Convert(source, &out); err != nil {
		return "", err
	}
	rewritten, err := rewrite(out.Bytes(), opts)
	if err != nil {
		return "", err
	}
	return policy.Sanitize(rewritten), nil
}

// rewrite applies the image and link rewrites to rendered HTML, before it is
// sanitized.
func rewrite(rendered []byte, opts Options) (string, error) {
	body := &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := xhtml.ParseFragment(bytes.NewReader(rendered), body)
	if err != nil {
		return "", err
	}
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			switch n.DataAtom {
			case atom.A:
				if hasAttr(n, "href") {
					setAttr(n, "target", "_blank")
					setAttr(n, "rel", "noopener noreferrer")
				}
			case atom.Img:
				if path, ok := RepoImage(opts.Directory, opts.Root, attr(n, "src")); ok {
					removeAttr(n, "src")
					removeAttr(n, "srcset")
					setAttr(n, "data-repo-image", path)
				}
			case atom.Source:
				if path, ok := RepoImage(opts.Directory, opts.Root, firstSrcsetURL(attr(n, "srcset"))); ok {
					removeAttr(n, "srcset")
					setAttr(n, "data-repo-srcset", path)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	var out strings.Builder
	for _, n := range nodes {
		walk(n)
		if err := xhtml.Render(&out, n); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

var schemePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)

// RepoLink is the absolute repository path a relative link in a document in
// directory points at (docs/guide.md, ../LICENSE), as the web viewer's
// resolveRepoLink does: not a link with a scheme or host, a page-local
// #anchor, a path starting with "/", or one that climbs out of the root.
func RepoLink(directory, href string) (string, bool) {
	link := strings.TrimSpace(href)
	if link == "" || strings.HasPrefix(link, "#") || strings.HasPrefix(link, "/") || schemePattern.MatchString(link) {
		return "", false
	}
	relative := link
	if i := strings.IndexAny(relative, "?#"); i >= 0 {
		relative = relative[:i]
	}
	if decoded, err := url.PathUnescape(relative); err == nil {
		relative = decoded
	}
	if relative == "" {
		return "", false
	}
	var parts []string
	for _, part := range strings.Split(directory, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	for _, part := range strings.Split(strings.ReplaceAll(relative, `\`, "/"), "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) == 0 {
				return "", false
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, part)
		}
	}
	return "/" + strings.Join(parts, "/"), true
}

// RepoImage is RepoLink for an image, which also resolves a root-absolute
// path ("/img/logo.png") against the project root when there is one, as the
// web viewer's resolveRepoImage does.
func RepoImage(directory, root, src string) (string, bool) {
	link := strings.TrimSpace(src)
	if strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "//") {
		if root == "" {
			return "", false
		}
		resolved, ok := RepoLink(root, strings.TrimLeft(link, "/"))
		if !ok {
			return "", false
		}
		prefix := root + "/"
		if root == "/" {
			prefix = "/"
		}
		if resolved != root && !strings.HasPrefix(resolved, prefix) {
			return "", false
		}
		return resolved, true
	}
	return RepoLink(directory, link)
}

func firstSrcsetURL(srcset string) string {
	first := strings.TrimSpace(strings.Split(srcset, ",")[0])
	if fields := strings.Fields(first); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *xhtml.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return true
		}
	}
	return false
}

func setAttr(n *xhtml.Node, key, value string) {
	for i, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			n.Attr[i].Val = value
			return
		}
	}
	n.Attr = append(n.Attr, xhtml.Attribute{Key: key, Val: value})
}

func removeAttr(n *xhtml.Node, key string) {
	kept := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Namespace != "" || a.Key != key {
			kept = append(kept, a)
		}
	}
	n.Attr = kept
}
