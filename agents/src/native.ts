// Changesets that agents push straight to Gitslice (git push origin
// HEAD:refs/changes/new), with no Artifacts repository in between. The Worker
// still reviews them, tells the Hub about them, and runs fixers for them.
//
// Gitslice is the source of truth here: the agent submits its own changeset
// once the reviewer identity has approved it, and the Hub notices the landing.

import type { Env } from "./env";
import { Gitslice, type Changeset, type PatchsetInfo, type SliceRef } from "./gitslice";
import { errorMessage, mapLimit, protectedPatterns } from "./land";
import { review, type ReviewFile } from "./review";
import type { Session, Signal } from "./types";

const MAX_FILES = 40;

export interface NativeHub {
  registerNative(input: {
    account: string;
    slice: string;
    changeset: { id: string; handle?: string; title: string; author?: string; baseCommitId: string };
    patchset: { id: string; number?: string; createdAtMs: number; changedPaths: string[]; baseTreeId?: string };
    force?: boolean;
  }): Promise<Session | null>;
  patch(id: string, partial: Partial<Session>, signal?: Omit<Signal, "at">, eventMessage?: string): Promise<Session | null>;
  touch(id: string, paths: string[]): Promise<void>;
}

// findChange resolves the id an agent has (the short handle git prints, or a
// full changeset id) to the changeset and its current patchset.
export async function findChange(gitslice: Gitslice, slice: SliceRef, idOrHandle: string): Promise<{ cs: Changeset; ps: PatchsetInfo } | null> {
  const wanted = idOrHandle.replace(/^cs_/, "");
  for (const status of ["draft", "submitted"]) {
    for (const cs of await gitslice.listChangesets(slice, status, 200)) {
      if (cs.id === idOrHandle || cs.handle === idOrHandle || cs.id.replace(/^cs_/, "").startsWith(wanted)) {
        const ps = cs.patchsets?.find((p) => p.id === cs.currentPatchsetId) ?? cs.patchsets?.at(-1);
        return ps ? { cs, ps } : null;
      }
    }
  }
  return null;
}

// reviewNativeChange registers a pushed patchset with the Hub and reviews it.
// Both the poller and an agent's nudge call it; the Hub's registration makes
// the second call for the same patchset a no-op.
export async function reviewNativeChange(env: Env, hub: NativeHub, slice: SliceRef, idOrHandle: string, force = false): Promise<void> {
  const gitslice = new Gitslice(env.GITSLICE_API, env.GITSLICE_TOKEN);
  const found = await findChange(gitslice, slice, idOrHandle);
  if (!found || found.cs.status !== "draft") return;
  const { cs, ps } = found;
  if (cs.description?.includes("Artifacts session:")) return; // that path has its own pipeline
  const session = await hub.registerNative({
    account: slice.account,
    slice: slice.slice,
    changeset: { id: cs.id, handle: cs.handle, title: cs.title ?? "", author: cs.author, baseCommitId: ps.baseCommitId || cs.baseCommitId || "" },
    patchset: { id: ps.id, number: ps.number, createdAtMs: Date.parse(ps.createdAt ?? "") || Date.now(), changedPaths: ps.changedPaths ?? cs.affectedPaths ?? [], baseTreeId: ps.baseTreeId },
    force,
  });
  if (!session) return;
  try {
    await reviewRegistered(env, hub, gitslice, slice, session, cs, ps);
  } catch (err) {
    console.error(JSON.stringify({ msg: "native review failed", session: session.id, error: errorMessage(err) }));
    await hub.patch(session.id, { status: "failed" }, { kind: "error", message: errorMessage(err) }, `failed: ${errorMessage(err).slice(0, 120)}`);
  }
}

// readNativeFiles reads what a patchset changes: each file's content in the
// base commit and in the patchset's result tree.
export async function readNativeFiles(gitslice: Gitslice, slice: SliceRef, cs: Changeset, ps: PatchsetInfo): Promise<ReviewFile[]> {
  const paths = (ps.changedPaths ?? cs.affectedPaths ?? []).slice(0, MAX_FILES);
  // A patchset can sit on a newer base than its changeset (an agent rebased).
  // Compare against the patchset's own base, or the diff would include other
  // agents' landed work.
  const base = ps.baseCommitId || cs.baseCommitId || "";
  const decoder = new TextDecoder();
  return mapLimit(paths, 6, async (path) => {
    const [before, after] = await Promise.all([
      ps.baseTreeId ? gitslice.readFileInTree(ps.baseTreeId, base, path, slice) : gitslice.readFile(base, path, slice),
      ps.resultTreeId ? gitslice.readFileInTree(ps.resultTreeId, base, path, slice) : null,
    ]);
    return { path, before: before ? decoder.decode(before) : null, after: after ? decoder.decode(after) : null };
  });
}

async function reviewRegistered(env: Env, hub: NativeHub, gitslice: Gitslice, slice: SliceRef, session: Session, cs: Changeset, ps: PatchsetInfo): Promise<void> {
  const files = await readNativeFiles(gitslice, slice, cs, ps);
  await hub.touch(session.id, ps.changedPaths ?? []);
  await hub.patch(session.id, { status: "reviewing" }, { kind: "info", message: `Reviewing patchset ${ps.number ?? ""} of ${cs.handle ?? cs.id} (${files.length} file(s)).` }, `reviewing ${cs.handle ?? cs.id}`);
  const verdict = await review(env.AI, {
    agent: session.agent,
    task: session.task,
    context: session.fixerFor ? "A fixer agent repaired this change after the author did not answer its review." : "",
    files,
    protectedPatterns: protectedPatterns(env),
  });
  if (verdict.verdict === "escalate") {
    await hub.patch(session.id, { status: "needs-human", review: verdict }, { kind: "escalated", message: `Needs a human review: ${verdict.summary}`, data: verdict.concerns }, "escalated to a human");
    return;
  }
  if (verdict.verdict === "request_changes") {
    await hub.patch(session.id, { status: "changes-requested", review: verdict }, { kind: "review", message: `Changes requested: ${verdict.summary}`, data: verdict.concerns }, "review requested changes");
    return;
  }
  // Approve as the reviewer identity; the agent submits once it sees this.
  await new Gitslice(env.GITSLICE_API, env.GITSLICE_REVIEWER_TOKEN).approve(cs.id);
  await hub.patch(session.id, { status: "approved", review: verdict }, { kind: "review", message: `Approved (${verdict.model}): ${verdict.summary}. Submit when ready.` }, `approved by ${verdict.model}`);
}
