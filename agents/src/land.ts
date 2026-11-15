// The landing pipeline: an agent's push to its Artifacts repo becomes a
// Gitslice changeset, gets reviewed, and lands in the slice's history. When
// another agent landed on the same files first, the bridge merges line by line
// and resubmits; only overlapping edits go back to the agent.

import { diffTrees, gitMode, readBlob, type TreeChange } from "./artifacts";
import type { Env } from "./env";
import { Gitslice, GitsliceError, sleep, type FileEdit, type SliceRef } from "./gitslice";
import { isText, merge3, type ConflictHunk } from "./merge";
import { review, type ReviewFile } from "./review";
import type { Session, Signal } from "./types";

export interface HubPort {
  getSession(id: string): Promise<Session | null>;
  claimPush(id: string, commit: string): Promise<Session | null>;
  patch(id: string, partial: Partial<Session>, signal?: Omit<Signal, "at">, eventMessage?: string): Promise<Session | null>;
  touch(id: string, paths: string[]): Promise<void>;
  landed(id: string, commit: string): Promise<void>;
}

export async function landPush(env: Env, hub: HubPort, sessionId: string, commit: string, retry = false): Promise<void> {
  let session: Session | null;
  if (retry) {
    // The Hub saw a landing stall, probably with a Worker invocation that
    // died. Pick it up where the record says it stopped.
    session = await hub.getSession(sessionId);
    if (!session || session.lastCommit !== commit || session.supersededBy || ["landed", "failed", "changes-requested", "conflict"].includes(session.status)) return;
    // A person's escalations stay with the person; a review nobody could do does not.
    if (session.status === "needs-human" && session.review?.model !== "none") return;
    if (session.changesetId) {
      const done = await new Gitslice(env.GITSLICE_API, env.GITSLICE_TOKEN).getChangeset(session.changesetId).catch(() => null);
      if (done?.status === "submitted" && done.commitId) {
        await hub.landed(sessionId, done.commitId);
        return;
      }
    }
  } else {
    session = await hub.claimPush(sessionId, commit);
  }
  if (!session) return; // already handled, or superseded by a newer session
  try {
    await landClaimed(env, hub, session, commit);
  } catch (err) {
    console.error(JSON.stringify({ msg: "landing failed", session: sessionId, error: errorMessage(err) }));
    await hub.patch(sessionId, { status: "failed" }, { kind: "error", message: errorMessage(err) }, `failed: ${errorMessage(err).slice(0, 120)}`);
  }
}

async function landClaimed(env: Env, hub: HubPort, session: Session, commit: string): Promise<void> {
  const slice: SliceRef = { account: session.account, slice: session.slice };
  const prefix = `${session.account}/${session.slice}/`;
  const gitslice = new Gitslice(env.GITSLICE_API, env.GITSLICE_TOKEN);

  using repo = await env.ARTIFACTS.get(session.id);
  const meta = await repo.readCommit(commit);
  if (!meta) throw new Error(`commit ${commit} is not in ${session.id}`);
  const changes = (await diffTrees(repo, session.baseline.tree, meta.treeHash)).filter((c) => c.path.startsWith(prefix));
  if (changes.length === 0) {
    await hub.patch(session.id, { status: "forked" }, { kind: "info", message: "That push changed nothing inside the slice." });
    return;
  }
  await hub.touch(
    session.id,
    changes.map((c) => `/${c.path}`),
  );

  // Upload the new contents and build the edit list.
  const contents = new Map<string, Uint8Array>();
  const edits = await mapLimit(changes, 6, async (change): Promise<FileEdit> => {
    if (change.op === "delete") return { op: "delete", path: `/${change.path}` };
    const data = await readBlob(repo, change.hash!);
    contents.set(change.path, data);
    const uploaded = await gitslice.uploadBlob(slice, data);
    return { op: "upsert", path: `/${change.path}`, blobId: uploaded.blobId, contentHash: uploaded.contentHash, mode: gitMode(change.mode) };
  });

  // One changeset per line of work; every push is a new patchset.
  const context = agentNotes(meta.message);
  let changesetId = session.changesetId;
  let handle = session.handle;
  if (!changesetId) {
    const created = await gitslice.createChangeset(slice, session.baseline.nativeCommit, title(session), describe(session, commit, meta.message));
    changesetId = created.id;
    handle = created.handle || created.id;
  }
  const patchset = await gitslice.updateChangeset(changesetId, session.baseline.nativeCommit, edits);
  await hub.patch(
    session.id,
    { status: "reviewing", changesetId, handle, patchsetId: patchset.id },
    { kind: "info", message: `Changeset ${handle} patchset ${patchset.number ?? ""} opened with ${edits.length} file(s).`, data: { changeset: handle ?? null } },
    `opened changeset ${handle}`,
  );

  // Review: deterministic checks, protected paths, then the model.
  const files: ReviewFile[] = await mapLimit(changes, 6, async (change) => ({
    path: `/${change.path}`,
    before: change.baseHash ? decode(await readBlob(repo, change.baseHash)) : null,
    after: change.op === "delete" ? null : decode(contents.get(change.path)!),
  }));
  const verdict = await review(env.AI, {
    agent: session.agent,
    task: session.task,
    context,
    files,
    protectedPatterns: protectedPatterns(env),
  });
  if (verdict.verdict === "escalate") {
    await hub.patch(
      session.id,
      { status: "needs-human", review: verdict },
      { kind: "escalated", message: `Needs a human review: ${verdict.summary}`, data: verdict.concerns },
      "escalated to a human",
    );
    return;
  }
  if (verdict.verdict === "request_changes") {
    await hub.patch(
      session.id,
      { status: "changes-requested", review: verdict },
      { kind: "review", message: `Changes requested: ${verdict.summary}`, data: verdict.concerns },
      "review requested changes",
    );
    return;
  }
  const reviewer = new Gitslice(env.GITSLICE_API, env.GITSLICE_REVIEWER_TOKEN);
  await reviewer.approve(changesetId);
  await hub.patch(
    session.id,
    { status: "approved", review: verdict },
    { kind: "review", message: `Approved (${verdict.model}): ${verdict.summary}` },
    `approved by ${verdict.model}`,
  );

  await submitAndLand(env, hub, { ...session, changesetId, handle, patchsetId: patchset.id }, repo, changes, edits, contents);
}

async function submitAndLand(
  env: Env,
  hub: HubPort,
  session: Session,
  repo: ArtifactsRepo,
  changes: TreeChange[],
  edits: FileEdit[],
  contents: Map<string, Uint8Array>,
): Promise<void> {
  const gitslice = new Gitslice(env.GITSLICE_API, env.GITSLICE_TOKEN);
  const reviewer = new Gitslice(env.GITSLICE_API, env.GITSLICE_REVIEWER_TOKEN);
  const changesetId = session.changesetId!;
  let patchsetId = session.patchsetId!;
  let lastError: unknown;
  for (let attempt = 0; attempt < 8; attempt++) {
    try {
      await hub.patch(session.id, { status: "landing", patchsetId });
      await gitslice.submit(changesetId, patchsetId);
      const commit = await waitPublished(gitslice, changesetId);
      await hub.landed(session.id, commit);
      return;
    } catch (err) {
      if (!(err instanceof GitsliceError)) throw err;
      lastError = err;
      if (err.missingApproval) {
        await reviewer.approve(changesetId);
        continue;
      }
      // Someone landed on our files after our baseline. Merge line by line
      // onto the current head.
      const head = await gitslice.headCommit();
      await hub.patch(session.id, { status: "merging" }, { kind: "info", message: "Another agent landed on the same files first; merging line by line." }, "merging onto head");
      const merged = await mergeOntoHead(gitslice, repo, session, changes, edits, contents, head);
      if (merged.kind === "unrelated") {
        // The change we collided with is not on the ref yet: its publish is
        // still in flight. Wait for it, then submit again.
        await sleep(500 * (attempt + 1));
        continue;
      }
      if (merged.kind === "conflict") {
        await hub.patch(
          session.id,
          { status: "conflict" },
          {
            kind: "conflict",
            message: `Overlapping edits in ${merged.paths.map(shortPath).join(", ")}. Open a new session with resume="${session.id}" and redo the change on the latest code.`,
            data: {
              paths: merged.paths,
              hunks: merged.hunks.slice(0, 3).map((h) => ({ path: h.path, base: h.base, ours: h.ours, theirs: h.theirs })),
            },
          },
          "conflict: overlapping edits",
        );
        return;
      }
      if (merged.edits.length === 0) {
        // Every change the agent made is already on head.
        await gitslice.abandon(changesetId).catch(() => {});
        await hub.patch(session.id, { status: "landed", landedAt: Date.now() }, { kind: "landed", message: "Your change is already in the codebase; another agent landed the same edit. Closed the changeset." }, "already landed by another agent");
        return;
      }
      const patchset = await gitslice.updateChangeset(changesetId, head, merged.edits, patchsetId);
      patchsetId = patchset.id;
      edits = merged.edits;
      await reviewer.approve(changesetId);
      await hub.patch(
        session.id,
        { patchsetId, autoMerged: true },
        { kind: "merged", message: `Merged line by line with the latest ${merged.merged.map(shortPath).join(", ") || "files"}; resubmitting.` },
        "auto-merged",
      );
    }
  }
  throw lastError ?? new Error("could not land the changeset after several merges");
}

type MergeOutcome =
  | { kind: "merged"; edits: FileEdit[]; merged: string[] }
  | { kind: "conflict"; paths: string[]; hunks: Array<ConflictHunk & { path: string }> }
  | { kind: "unrelated" };

async function mergeOntoHead(
  gitslice: Gitslice,
  repo: ArtifactsRepo,
  session: Session,
  changes: TreeChange[],
  edits: FileEdit[],
  contents: Map<string, Uint8Array>,
  head: string,
): Promise<MergeOutcome> {
  const slice: SliceRef = { account: session.account, slice: session.slice };
  const out: FileEdit[] = [];
  const merged: string[] = [];
  const conflicts: string[] = [];
  const hunks: Array<ConflictHunk & { path: string }> = [];
  for (let i = 0; i < changes.length; i++) {
    const change = changes[i];
    const path = `/${change.path}`;
    const theirs = await gitslice.readFile(head, path, slice);
    const base = change.baseHash ? await readBlob(repo, change.baseHash) : null;
    if (sameBytes(theirs, base)) {
      out.push(edits[i]); // nobody else touched this file
      continue;
    }
    const ours = change.op === "delete" ? null : contents.get(change.path)!;
    if (sameBytes(theirs, ours)) continue; // the same change is already there
    if (ours === null || theirs === null || !isText(ours) || !isText(theirs) || (base && !isText(base))) {
      conflicts.push(path);
      continue;
    }
    const result = merge3(base ? decode(base) : "", decode(ours), decode(theirs));
    if (!result.clean) {
      conflicts.push(path);
      for (const hunk of result.conflicts) hunks.push({ path, ...hunk });
      continue;
    }
    const data = new TextEncoder().encode(result.text);
    const uploaded = await gitslice.uploadBlob(slice, data);
    out.push({ op: "upsert", path, blobId: uploaded.blobId, contentHash: uploaded.contentHash, mode: gitMode(change.mode) });
    merged.push(path);
  }
  if (conflicts.length > 0) return { kind: "conflict", paths: conflicts, hunks };
  if (merged.length === 0 && out.length === edits.length) return { kind: "unrelated" };
  return { kind: "merged", edits: out, merged };
}

// retryEscalated resubmits a change a human may have approved since.
export async function retryEscalated(env: Env, hub: HubPort, session: Session): Promise<void> {
  if (!session.changesetId || !session.patchsetId) return;
  const gitslice = new Gitslice(env.GITSLICE_API, env.GITSLICE_TOKEN);
  try {
    await gitslice.submit(session.changesetId, session.patchsetId);
  } catch (err) {
    if (err instanceof GitsliceError && err.missingApproval) return; // still waiting
    await hub.patch(session.id, { status: "conflict" }, { kind: "conflict", message: errorMessage(err) }, "escalated change could not land");
    return;
  }
  await hub.patch(session.id, { status: "landing" }, { kind: "review", message: "A human approved the change." }, "approved by a human");
  const commit = await waitPublished(gitslice, session.changesetId);
  await hub.landed(session.id, commit);
}

export async function waitPublished(gitslice: Gitslice, changesetId: string): Promise<string> {
  for (let i = 0; i < 120; i++) {
    const cs = await gitslice.getChangeset(changesetId);
    if (cs.status === "submitted" && cs.commitId) return cs.commitId;
    if (cs.status === "abandoned") throw new Error("changeset was abandoned");
    await sleep(i < 10 ? 250 : 1000);
  }
  throw new Error("timed out waiting for the changeset to publish");
}

function title(session: Session): string {
  return `${session.agent}: ${session.task}`.slice(0, 120);
}

function describe(session: Session, commit: string, message: string): string {
  return [
    `Agent: ${session.agent}`,
    `Task: ${session.task}`,
    `Artifacts session: ${session.id}`,
    `Pushed commit: ${commit}`,
    `Baseline: ${session.baseline.repo} at ${session.baseline.nativeCommit}`,
    "",
    "Agent notes:",
    agentNotes(message) || "(none)",
  ].join("\n");
}

// agentNotes is the commit message body: the agent's reasoning and trailers.
function agentNotes(message: string): string {
  return message.split("\n").slice(1).join("\n").trim();
}

export function protectedPatterns(env: Env): RegExp[] {
  return (env.PROTECTED_PATHS || "")
    .split(",")
    .map((p) => p.trim())
    .filter(Boolean)
    .map((p) => new RegExp(p.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*")));
}

function sameBytes(a: Uint8Array | null, b: Uint8Array | null): boolean {
  if (a === null || b === null) return a === b;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

function decode(data: Uint8Array): string {
  return new TextDecoder().decode(data);
}

function shortPath(path: string): string {
  return path.split("/").slice(3).join("/") || path;
}

export function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export async function mapLimit<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length);
  let next = 0;
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) {
      const index = next++;
      out[index] = await fn(items[index]);
    }
  });
  await Promise.all(workers);
  return out;
}
