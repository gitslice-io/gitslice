import { marked } from "marked";
import { describe, expect, it } from "vitest";

import { blogPostIndex, getPostMeta, loadPost } from "./postIndex";
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

  it("include the benchmark results against git", () => {
    const post = getPost("benchmarking-gitslice-against-git");
    expect(post?.markdown).toContain("| Scenario | git, 1k | gs, 1k | git, 10k | gs, 10k |");
    expect(post?.markdown).toContain("GITSLICE_BENCH=1");
    const html = marked.parse(post?.markdown ?? "", { async: false });
    expect(html).toContain('<th align="right">gs, 10k</th>');
    expect(html).toContain('<code class="language-bash">GITSLICE_TEST_DATABASE_URL=...');
    expect(html).toContain('<td align="right">455 s</td>');
  });

  it("include the Pando post", () => {
    const post = getPost("pando-style-git-covers");
    expect(post?.markdown).toContain("| Cold clone of `gitslice/gitslice` | Before | After |");
    expect(post?.markdown).toContain("git repack -a -d -b");
    const html = marked.parse(post?.markdown ?? "", { async: false });
    expect(html).toContain('<a href="https://openai.com/index/scaling-git-at-openai/">Pando</a>');
    expect(html).toContain('<td align="right"><strong>5.1 s</strong></td>');
    expect(html).toContain("<th>CI checkout of the Gitslice tree</th>");
  });

  it("include the Git mirrors post", () => {
    const post = getPost("slice-git-mirrors");
    expect(post?.markdown).toContain("gs slice mirror set acme/app --url https://github.com/acme/app.git");
    const html = marked.parse(post?.markdown ?? "", { async: false });
    expect(html).toContain('<a href="/doc/mirrors">Git Mirrors docs</a>');
    for (const internal of ["Cloud Run", "object storage", "R2", "signed request", "design/35"]) {
      expect(post?.markdown).not.toContain(internal);
    }
    expect(html).toContain('<code class="language-bash">gs slice secret set acme/app GITHUB_MIRROR_TOKEN');
  });

  it("include the ABFS post first", () => {
    const post = getPost("what-we-learned-from-abfs");
    expect(blogPosts[0]).toBe(post);
    expect(post?.markdown).toContain("| `rm -rf` them | 0.2 s | 15.9 s | **2.6 s** |");
    expect(post?.markdown).toContain("gs cache prune --max 2G --dry-run");
    const html = marked.parse(post?.markdown ?? "", { async: false });
    expect(html).toContain('<a href="/doc/lazy">Lazy Workspaces docs</a>');
    expect(html).toContain('<td align="right"><strong>36 KB</strong>, only what changed</td>');
    for (const internal of ["Cloud Run", "Postgres", "~/.cache", "design/", "since_commit_id", "lazyfs", "treestore"]) {
      expect(post?.markdown).not.toContain(internal);
    }
  });

  it("exposes metadata-only postIndex and lazy per-slug loadPost", async () => {
    expect(blogPostIndex.map((m) => m.slug)).toEqual(blogPosts.map((p) => p.slug));
    for (const meta of blogPostIndex) {
      expect("markdown" in meta).toBe(false);
      expect(getPostMeta(meta.slug)).toEqual(meta);
      const loaded = await loadPost(meta.slug);
      expect(loaded).toEqual(getPost(meta.slug));
    }
    expect(await loadPost("nonexistent-slug")).toBeUndefined();
  });
});
