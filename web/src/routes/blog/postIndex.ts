export interface BlogPostMeta {
  slug: string;
  title: string;
  dek: string;
  date: string;
  readingMinutes: number;
  tags: string[];
}

export interface BlogPost extends BlogPostMeta {
  markdown: string;
}

export const blogPostIndex: BlogPostMeta[] = [
  {
    slug: "what-we-learned-from-abfs",
    title: "What we learned from Google's ABFS: smaller syncs, a bounded cache, faster deletes",
    dek: "We compared lazy workspaces with the virtual file system Android engineers use for huge source trees. Syncs now transfer only changed files, local caches stay bounded, and deleting 10,000 files in a mount dropped from 15.9 s to 2.6 s.",
    date: "2026-11-18",
    readingMinutes: 6,
    tags: ["performance", "lazy-workspaces"]
  },
  {
    slug: "slice-git-mirrors",
    title: "Back up any slice to GitHub with one setting",
    dek: "Keep an automatic copy of any slice in a GitHub repository — or any Git host. Every landing and tag pushes within seconds, with nothing to maintain and no force-pushes.",
    date: "2026-11-14",
    readingMinutes: 4,
    tags: ["git", "feature"]
  },
  {
    slug: "pando-style-git-covers",
    title: "What we borrowed from OpenAI's Pando: a cold clone from a timeout to 5 seconds",
    dek: "Fresh clones used to read every historical file on the fly and time out past 100 s. Prebuilt compressed covers and single-request CI checkouts fixed it: 5.1 s for a cold clone, and 1 request for a CI checkout.",
    date: "2026-11-03",
    readingMinutes: 6,
    tags: ["performance", "git"]
  },
  {
    slug: "benchmarking-gitslice-against-git",
    title: "Benchmarking Gitslice against Git: where we win, where we lose, and what 455 seconds taught us",
    dek: "We timed import, commit, and checkout against Git on identical fixtures. Focused slice checkouts already beat Git, bulk import is still 3x slower, and removing per-file round trips cut a real-network checkout from 455 s to 2.7 s.",
    date: "2026-10-15",
    readingMinutes: 6,
    tags: ["performance", "benchmarks"]
  },
  {
    slug: "agent-scale-source-control",
    title: "Source control at agent scale: Scion, Origin, and Gitslice",
    dek: "Anthropic built an internal VCS for coding agents, and Cursor built a Git forge around them. Here is where Gitslice fits — slices over one source graph, Git at the edges, open source — and how to point your agents at it.",
    date: "2026-10-13",
    readingMinutes: 5,
    tags: ["agents", "architecture"]
  },
  {
    slug: "native-source-graph",
    title: "A native source graph, with Git at the boundary",
    dek: "Gitslice does not run a Git server underneath. Inside, it is one content-addressed source graph indexed for overlapping views and per-path concurrency, with Git projected at the edges.",
    date: "2026-08-19",
    readingMinutes: 6,
    tags: ["architecture", "storage"]
  }
];

const postMarkdownLoaders: Record<string, () => Promise<{ markdown: string }>> = {
  "what-we-learned-from-abfs": () => import("./content/what-we-learned-from-abfs"),
  "slice-git-mirrors": () => import("./content/slice-git-mirrors"),
  "pando-style-git-covers": () => import("./content/pando-style-git-covers"),
  "benchmarking-gitslice-against-git": () => import("./content/benchmarking-gitslice-against-git"),
  "agent-scale-source-control": () => import("./content/agent-scale-source-control"),
  "native-source-graph": () => import("./content/native-source-graph")
};

export function getPostMeta(slug: string): BlogPostMeta | undefined {
  return blogPostIndex.find((post) => post.slug === slug);
}

export async function loadPostMarkdown(slug: string): Promise<string | undefined> {
  const loader = postMarkdownLoaders[slug];
  if (!loader) {
    return undefined;
  }
  const mod = await loader();
  return mod.markdown;
}

export async function loadPost(slug: string): Promise<BlogPost | undefined> {
  const meta = getPostMeta(slug);
  if (!meta) {
    return undefined;
  }
  const markdown = await loadPostMarkdown(slug);
  if (markdown === undefined) {
    return undefined;
  }
  return { ...meta, markdown };
}
