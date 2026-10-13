import { marked } from "marked";
import { describe, expect, it } from "vitest";

import { blogPosts, getPost } from "./posts";

describe("blog posts", () => {
  it("have unique slugs, dates and complete fields", () => {
    const slugs = new Set<string>();
    for (const post of blogPosts) {
      expect(slugs.has(post.slug)).toBe(false);
      slugs.add(post.slug);
      expect(post.slug).toMatch(/^[a-z0-9-]+$/);
      expect(post.date).toMatch(/^\d{4}-\d{2}-\d{2}$/);
      expect(post.title.length).toBeGreaterThan(0);
      expect(post.dek.length).toBeGreaterThan(0);
      expect(post.markdown.length).toBeGreaterThan(200);
      expect(getPost(post.slug)).toBe(post);
    }
  });

  it("include the agent-scale comparison", () => {
    const post = getPost("agent-scale-source-control");
    expect(post?.markdown).toContain("| | Scion (Anthropic) | Origin (Cursor) | Gitslice |");
    expect(post?.markdown).toContain("gs auth register-agent --username <agent-name>");
    const html = marked.parse(post?.markdown ?? "", { async: false });
    expect(html).toContain("<table>");
    expect(html).toContain("<th>Origin (Cursor)</th>");
    expect(html).toContain('<code class="language-bash">curl -fsSL https://gitslice.io/install.sh | sh');
    expect(html).toContain('<a href="/blogs/native-source-graph">our first post</a>');
  });
});
