// restoreSeed puts a demo slice back the way it was seeded, so the swarm can
// run again on a clean codebase. It is an operator action, not an agent
// change: one changeset that writes the seed's own files back under the slice
// root and deletes what agents added, approved by the reviewer identity.

import type { Env } from "./env";
import { Gitslice, GitsliceError, type Commit, type FileEdit, type SliceRef, type TreeEntry } from "./gitslice";
import { waitPublished } from "./land";

export interface RestoreResult {
  seed: string;
  changed: number;
  handle?: string;
  commit?: string;
}

export async function restoreSeed(env: Env, slice: SliceRef): Promise<RestoreResult> {
  const bridge = new Gitslice(env.GITSLICE_API, env.GITSLICE_TOKEN);
  const reviewer = new Gitslice(env.GITSLICE_API, env.GITSLICE_REVIEWER_TOKEN);
  const root = `/${slice.account}/${slice.slice}`;
  const { seed, want } = await findSeed(bridge, slice, root);

  for (let attempt = 0; ; attempt++) {
    const head = await bridge.headCommit();
    const have = await files(bridge, slice, head, root);
    const edits: FileEdit[] = [];
    for (const [path, entry] of want) {
      const current = have.get(path);
      if (!current || current.blobId !== entry.blobId || current.mode !== entry.mode) {
        edits.push({ op: "upsert", path, blobId: entry.blobId, contentHash: entry.contentHash, mode: entry.mode });
      }
    }
    for (const path of have.keys()) if (!want.has(path)) edits.push({ op: "delete", path });
    if (edits.length === 0) return { seed: seed.id, changed: 0 };

    const changeset = await bridge.createChangeset(
      slice,
      head,
      "demo: restore the seed",
      `Puts ${root} back to ${seed.id} (${seed.message ?? "the seed"}) so the agent swarm can run again on a clean codebase.`,
    );
    try {
      const patchset = await bridge.updateChangeset(changeset.id, head, edits);
      await reviewer.approve(changeset.id);
      await bridge.submit(changeset.id, patchset.id);
      const commit = await waitPublished(bridge, changeset.id);
      return { seed: seed.id, changed: edits.length, handle: changeset.handle || changeset.id, commit };
    } catch (err) {
      await bridge.abandon(changeset.id).catch(() => {});
      // An agent landed in between: start over from the new head.
      if (attempt < 3 && err instanceof GitsliceError && !err.missingApproval) continue;
      throw err;
    }
  }
}

// findSeed returns the oldest commit with files under the slice root (the
// slice's first commit only creates the directory) and those files.
async function findSeed(gitslice: Gitslice, slice: SliceRef, root: string): Promise<{ seed: Commit; want: Map<string, TreeEntry> }> {
  const commits = await gitslice.listCommits(slice);
  for (const commit of commits.reverse()) {
    const want = await files(gitslice, slice, commit.id, root);
    if (want.size > 0) return { seed: commit, want };
  }
  throw new Error(`${root} has no seed commit`);
}

// files lists every file under path at a commit, keyed by global path.
async function files(gitslice: Gitslice, slice: SliceRef, commitId: string, path: string): Promise<Map<string, TreeEntry>> {
  const out = new Map<string, TreeEntry>();
  const walk = async (dir: string): Promise<void> => {
    const entries = await gitslice.listDirectory(commitId, dir, slice);
    await Promise.all(
      entries.map(async (entry) => {
        if (entry.kind === "ENTRY_KIND_DIRECTORY") await walk(entry.path);
        else if (entry.blobId) out.set(entry.path, entry);
      }),
    );
  };
  await walk(path);
  return out;
}
