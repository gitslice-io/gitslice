import { markdown as automationsMarkdown } from "./content/folder-automations";
import { markdown as agentScaleMarkdown } from "./content/agent-scale-source-control";
import { markdown as benchmarkMarkdown } from "./content/benchmarking-gitslice-against-git";
import { markdown as nativeGraphMarkdown } from "./content/native-source-graph";
import { markdown as pandoMarkdown } from "./content/pando-style-git-covers";
import { markdown as mirrorsMarkdown } from "./content/slice-git-mirrors";
import { markdown as abfsMarkdown } from "./content/what-we-learned-from-abfs";
import { blogPostIndex, type BlogPost, type BlogPostMeta } from "./postIndex";

export type { BlogPost, BlogPostMeta };

const markdownBySlug: Record<string, string> = {
  "folder-automations": automationsMarkdown,
  "what-we-learned-from-abfs": abfsMarkdown,
  "slice-git-mirrors": mirrorsMarkdown,
  "pando-style-git-covers": pandoMarkdown,
  "benchmarking-gitslice-against-git": benchmarkMarkdown,
  "agent-scale-source-control": agentScaleMarkdown,
  "native-source-graph": nativeGraphMarkdown
};

export const blogPosts: BlogPost[] = blogPostIndex.map((meta) => ({
  ...meta,
  markdown: markdownBySlug[meta.slug] ?? ""
}));

export function getPost(slug: string): BlogPost | undefined {
  return blogPosts.find((post) => post.slug === slug);
}
