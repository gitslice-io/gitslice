import { describe, expect, it } from "vitest";

import { codeInfo, headingId, renderPost } from "./renderPost";
import { blogPostIndex, loadPostMarkdown } from "./postIndex";

describe("renderPost", () => {
  it("gives headings anchors and lists the sections", () => {
    const { html, headings } = renderPost(
      "## 1. One slice, one mirror\n\ntext\n\n## 2. What gets `pushed`\n\n## 2. What gets `pushed`",
    );
    expect(html).toContain(
      '<h2 id="one-slice-one-mirror" class="blog-heading"><a class="blog-anchor" href="#one-slice-one-mirror"',
    );
    expect(headings.map((heading) => heading.id)).toEqual([
      "one-slice-one-mirror",
      "what-gets-pushed",
      "what-gets-pushed-2",
    ]);
    expect(headings[1].text).toBe("2. What gets pushed");
  });

  it("frames code blocks with their language and a copy button, escaping the code", () => {
    const { html } = renderPost(
      "```bash\necho '<b>' && ls\n```\n\n```yaml title=.gitslice/automations.yaml\non: push\n```",
    );
    expect(html).toContain('<span class="blog-code-title">Shell</span>');
    expect(html).toContain(`<code class="language-bash">echo '&lt;b&gt;' &amp;&amp; ls`);
    expect(html).toContain(
      '<span class="blog-code-title">.gitslice/automations.yaml</span><span class="blog-code-lang">YAML</span>',
    );
    expect(html.match(/data-copy/g)).toHaveLength(2);
  });

  it("leaves mermaid blocks to be drawn", () => {
    expect(renderPost("```mermaid\nflowchart LR\n```").html).toContain(
      '<pre class="blog-mermaid"><code class="language-mermaid">',
    );
  });

  it("opens only other sites in a new tab", () => {
    const { html } = renderPost(
      "[docs](/doc/automations) [home](https://gitslice.io/blogs) [cf](https://developers.cloudflare.com/)",
    );
    expect(html).toContain('<a href="/doc/automations">docs</a>');
    expect(html).toContain('<a href="https://gitslice.io/blogs">home</a>');
    expect(html).toContain(
      '<a href="https://developers.cloudflare.com/" target="_blank" rel="noopener noreferrer">cf</a>',
    );
  });

  it("renders every published post", async () => {
    for (const post of blogPostIndex) {
      const markdown = await loadPostMarkdown(post.slug);
      expect(markdown, post.slug).toBeTruthy();
      expect(
        renderPost(markdown ?? "").headings.length,
        post.slug,
      ).toBeGreaterThan(0);
    }
  });
});

describe("headingId / codeInfo", () => {
  it("slugs headings and reads fence info", () => {
    expect(headingId("10. Go &amp; <code>gs</code>!")).toBe("go-gs");
    expect(codeInfo('ts title="web/x.ts"')).toEqual({
      lang: "ts",
      title: "web/x.ts",
    });
    expect(codeInfo(undefined)).toEqual({ lang: "", title: "" });
  });
});
