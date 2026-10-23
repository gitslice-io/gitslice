package markdown

import (
	"strings"
	"testing"
)

func render(t *testing.T, source string, opts Options) string {
	t.Helper()
	out, err := Render([]byte(source), opts)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Nothing that runs script, loads a frame or posts a form survives, in raw
// HTML or in Markdown.
func TestRenderRemovesWhatCouldRunScript(t *testing.T) {
	attacks := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror="alert(2)">`,
		`[x](javascript:alert(3))`,
		`<a href="javascript:alert(4)">a</a>`,
		`<a href="https://e.com" onclick="evil()">ok</a>`,
		`<iframe src="https://evil"></iframe>`,
		`<svg><script>alert(5)</script></svg>`,
		`<svg onload="alert(6)"></svg>`,
		`<math><mi xlink:href="javascript:alert(7)">m</mi></math>`,
		`<form action="https://evil"><input name="a" type="text"></form>`,
		`<object data="x.swf"></object><embed src="x.swf">`,
		`<style>body{display:none}</style>`,
		`<p style="background:url(javascript:alert(8))">styled</p>`,
		`<base href="https://evil/">`,
		`<meta http-equiv="refresh" content="0;url=https://evil">`,
		`<img src="data:text/html;base64,PHNjcmlwdD5hbGVydCg5KTwvc2NyaXB0Pg==">`,
		`<a href="data:text/html,<script>alert(10)</script>">d</a>`,
		`<noscript><p title="</noscript><img src=x onerror=alert(11)>"></noscript>`,
		"<details open ontoggle=\"alert(12)\"><summary>s</summary>b</details>",
		`<img src="logo.png" data-repo-image="javascript:alert(13)">`,
		`<a href="https://e.com" target="_top" rel="opener">t</a>`,
		`<source srcset="x.png" onerror="alert(14)">`,
		`<input type="text" autofocus onfocus="alert(15)">`,
		`[link](  JaVaScRiPt:alert(16))`,
		`<a href="vbscript:msgbox(17)">v</a>`,
	}
	for _, attack := range attacks {
		out := strings.ToLower(render(t, attack, Options{Directory: "/acme/app", Root: "/acme/app"}))
		for _, bad := range []string{"<script", "onerror", "onclick", "onload", "onfocus", "ontoggle", "javascript:", "vbscript:", "<iframe", "<form", "<object", "<embed", "<style", "style=", "<base", "<meta", "data:text/html", "target=\"_top\"", "opener\""} {
			if strings.Contains(out, bad) {
				t.Errorf("%q rendered as %q, which contains %q", attack, out, bad)
			}
		}
		if strings.Contains(out, `<input`) && !strings.Contains(out, `type="checkbox"`) {
			t.Errorf("%q kept an input that is not a task-list checkbox: %q", attack, out)
		}
	}
}

// What READMEs use survives: GitHub-flavored Markdown and the raw HTML they
// put at the top.
func TestRenderKeepsWhatReadmesUse(t *testing.T) {
	source := strings.Join([]string{
		`<h1 align="center">Gitslice</h1>`,
		``,
		`<p align="center"><strong>Many agents.</strong></p>`,
		``,
		`| a | b |`,
		`|---|---|`,
		`| 1 | 2 |`,
		``,
		`- [x] done`,
		`- [ ] todo`,
		``,
		"```go",
		`func main() {}`,
		"```",
		``,
		`~~gone~~ and https://example.com/auto`,
		``,
		`<details><summary>More</summary>Hidden</details>`,
		``,
		`[site](https://gitslice.io) and [guide](docs/guide.md)`,
	}, "\n")
	out := render(t, source, Options{Directory: "/acme/app"})
	for _, want := range []string{
		`<h1 align="center">Gitslice</h1>`,
		`<p align="center"><strong>Many agents.</strong></p>`,
		`<table>`, `<td>1</td>`,
		`type="checkbox"`, `checked=""`,
		`<code class="language-go">`,
		`<del>gone</del>`,
		`<a href="https://example.com/auto"`,
		`<details><summary>More</summary>Hidden</details>`,
		`<a href="https://gitslice.io" target="_blank" rel="noopener noreferrer">site</a>`,
		// A relative link stays relative: the viewer opens it in the slice.
		`<a href="docs/guide.md" target="_blank" rel="noopener noreferrer">guide</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered README lacks %q:\n%s", want, out)
		}
	}
}

// An image in the repository is not requested from the page's address: its
// path goes to data-repo-image for the viewer to load. Other images keep
// their src.
func TestRenderMarksRepositoryImages(t *testing.T) {
	opts := Options{Directory: "/acme/app/docs", Root: "/acme/app"}
	cases := map[string]string{
		`![logo](logo.png)`:           `data-repo-image="/acme/app/docs/logo.png"`,
		`![up](../img/up.png)`:        `data-repo-image="/acme/app/img/up.png"`,
		`![root](/img/root.png)`:      `data-repo-image="/acme/app/img/root.png"`,
		`<img src="./shot.webp">`:     `data-repo-image="/acme/app/docs/shot.webp"`,
		`![web](https://e.com/x.png)`: `src="https://e.com/x.png"`,
	}
	for source, want := range cases {
		out := render(t, source, opts)
		if !strings.Contains(out, want) {
			t.Errorf("%q rendered as %q, want %q", source, out, want)
		}
		if strings.Contains(want, "data-repo-image") && strings.Contains(out, " src=") {
			t.Errorf("%q kept a src the page would request: %q", source, out)
		}
	}
	// Climbing out of the repository, or a root-absolute path with no
	// project root, is left as written (and so not loaded from the API).
	if out := render(t, `![x](../../../../../x.png)`, opts); strings.Contains(out, "data-repo-image") {
		t.Errorf("an image above the repository root was marked: %q", out)
	}
	if out := render(t, `![x](/img/x.png)`, Options{Directory: "/acme/app"}); strings.Contains(out, "data-repo-image") {
		t.Errorf("a root-absolute image with no project root was marked: %q", out)
	}
}

// A <picture> keeps its sources for the viewer to pick by the site's theme;
// a source in the repository carries its path in data-repo-srcset.
func TestRenderKeepsPictureSourcesForTheTheme(t *testing.T) {
	source := `<picture><source media="(prefers-color-scheme: dark)" srcset="logo-dark.png"><img src="logo.png" alt="Logo"></picture>`
	out := render(t, source, Options{Directory: "/acme/app"})
	for _, want := range []string{`<picture>`, `media="(prefers-color-scheme: dark)"`, `data-repo-srcset="/acme/app/logo-dark.png"`, `data-repo-image="/acme/app/logo.png"`, `alt="Logo"`} {
		if !strings.Contains(out, want) {
			t.Errorf("picture rendered as %q, want %q", out, want)
		}
	}
	if strings.Contains(out, `srcset="logo-dark.png"`) {
		t.Errorf("a repository srcset was left for the page to request: %q", out)
	}
}
