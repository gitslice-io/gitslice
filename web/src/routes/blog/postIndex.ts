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
    dek: "We compared lazy workspaces with the file system Android engineers use for their giant source tree. A sync now downloads only what changed, the local cache stays under a limit, and deleting 10,000 files in a mount went from 15.9 s to 2.6 s.",
    date: "2026-11-18",
    readingMinutes: 6,
    tags: ["performance", "lazy-workspaces"]
  },
  {
    slug: "slice-git-mirrors",
    title: "Back up any slice to GitHub with one setting",
    dek: "A slice can now keep a copy of itself in a GitHub repository, or any Git host. Every landing and tag is pushed within seconds, with nothing for you to run, and it never force-pushes.",
    date: "2026-11-14",
    readingMinutes: 4,
    tags: ["git", "feature"]
  },
  {
    slug: "pando-style-git-covers",
    title: "What we borrowed from OpenAI's Pando: a cold clone from a timeout to 5 seconds",
    dek: "A clone on a fresh server instance read every file of the history and compressed it for one client, and timed out. Stored, compressed packs (covers) and a one-call CI checkout fixed it: 5.1 seconds for the clone, one request for the checkout.",
    date: "2026-11-03",
    readingMinutes: 6,
    tags: ["performance", "git"]
  },
  {
    slug: "benchmarking-gitslice-against-git",
    title: "Benchmarking Gitslice against Git: where we win, where we lose, and what 455 seconds taught us",
    dek: "We timed import, commit and checkout against Git on the same files. A one-directory checkout beats Git; import is still 3x slower. And a 455-second checkout over a real network became 2.7 seconds by removing per-file round trips.",
    date: "2026-10-15",
    readingMinutes: 6,
    tags: ["performance", "benchmarks"]
  },
  {
    slug: "agent-scale-source-control",
    title: "Source control at agent scale: Scion, Origin and Gitslice",
    dek: "Anthropic built its own version control system for agents, and Cursor built a Git forge for them. Here is where Gitslice fits: slices over one source graph, Git at the edges, open source. And how to point your agents at it.",
    date: "2026-10-13",
    readingMinutes: 5,
    tags: ["agents", "architecture"]
  },
  {
    slug: "native-source-graph",
    title: "A native source graph, with Git at the boundary",
    dek: "Gitslice doesn't run a Git server. Inside, it's one content-addressed source graph; Git is a projection at the edge — here's the model, and why we bet on it.",
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
