// Helpers over the Artifacts Workers binding: tree diffs between an agent's
// push and the baseline it forked, and naming for baselines and sessions.

export interface TreeChange {
  op: "upsert" | "delete";
  path: string; // repository-relative, e.g. "demo/storefront/src/cart.ts"
  hash?: string; // new blob id (upsert)
  baseHash?: string; // blob id in the baseline, if the path existed there
  mode?: string; // Git mode of the new entry
}

type Repo = ArtifactsRepo;

// diffTrees lists the files that differ between two Git trees, descending only
// into subtrees whose ids differ. Gitlinks (submodules) are ignored.
export async function diffTrees(repo: Repo, base: string | null, head: string | null, prefix = ""): Promise<TreeChange[]> {
  if (base === head) return [];
  const [baseEntries, headEntries] = await Promise.all([
    base ? readTree(repo, base) : Promise.resolve([] as ArtifactsTreeEntry[]),
    head ? readTree(repo, head) : Promise.resolve([] as ArtifactsTreeEntry[]),
  ]);
  const baseByName = new Map(baseEntries.map((e) => [e.name, e]));
  const headByName = new Map(headEntries.map((e) => [e.name, e]));
  const work: Array<Promise<TreeChange[]>> = [];
  const out: TreeChange[] = [];

  for (const [name, entry] of headByName) {
    const path = prefix + name;
    const before = baseByName.get(name);
    if (entry.type === "gitlink") continue;
    if (entry.type === "tree") {
      const beforeTree = before?.type === "tree" ? before.hash : null;
      if (before && before.type !== "tree" && before.type !== "gitlink") {
        out.push({ op: "delete", path, baseHash: before.hash });
      }
      if (beforeTree !== entry.hash) work.push(diffTrees(repo, beforeTree, entry.hash, path + "/"));
      continue;
    }
    if (before?.type === "tree") {
      work.push(diffTrees(repo, before.hash, null, path + "/"));
    }
    const beforeBlob = before && before.type !== "tree" && before.type !== "gitlink" ? before : undefined;
    if (!beforeBlob || beforeBlob.hash !== entry.hash || beforeBlob.mode !== entry.mode) {
      out.push({ op: "upsert", path, hash: entry.hash, baseHash: beforeBlob?.hash, mode: entry.mode });
    }
  }
  for (const [name, entry] of baseByName) {
    if (headByName.has(name) || entry.type === "gitlink") continue;
    const path = prefix + name;
    if (entry.type === "tree") work.push(diffTrees(repo, entry.hash, null, path + "/"));
    else out.push({ op: "delete", path, baseHash: entry.hash });
  }
  for (const nested of await Promise.all(work)) out.push(...nested);
  return out.sort((a, b) => a.path.localeCompare(b.path));
}

async function readTree(repo: Repo, hash: string): Promise<ArtifactsTreeEntry[]> {
  const entries = await repo.readTree(hash);
  if (!entries) throw new Error(`tree ${hash} not found`);
  return entries;
}

export async function readBlob(repo: Repo, hash: string): Promise<Uint8Array> {
  const blob = await repo.readBlob(hash);
  if (!blob) throw new Error(`blob ${hash} not found`);
  return new Uint8Array(await blob.arrayBuffer());
}

// gitMode maps a Git mode string to the numeric mode Gitslice stores.
export function gitMode(mode: string | undefined): number {
  switch (mode) {
    case "100755":
      return 0o100755;
    case "120000":
      return 0o120000;
    default:
      return 0o100644;
  }
}

// nativeCommitOf reads the Gitslice commit a projected Git commit stands for
// from its "Gitslice-Commit:" trailer.
export function nativeCommitOf(message: string): string | null {
  const match = /^Gitslice-Commit:\s*(sha256:[0-9a-f]{64})\s*$/m.exec(message);
  return match ? match[1] : null;
}

// Repo names: letters, digits, ".", "_" and "-"; "." separates the parts
// because Gitslice account and slice names never contain it.
export function baselineRepoName(account: string, slice: string, nativeCommit: string): string {
  return `base.${account}.${slice}.${nativeCommit.replace("sha256:", "").slice(0, 16)}`;
}

export function sessionRepoName(account: string, slice: string, agent: string, nonce: string): string {
  return `s.${account}.${slice}.${slugify(agent)}.${nonce}`;
}

export function parseSessionRepo(name: string): { account: string; slice: string } | null {
  const parts = name.split(".");
  if (parts.length < 5 || parts[0] !== "s") return null;
  return { account: parts[1], slice: parts[2] };
}

export function slugify(text: string): string {
  return (
    text
      .toLowerCase()
      .replace(/[^a-z0-9-]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 40) || "agent"
  );
}
