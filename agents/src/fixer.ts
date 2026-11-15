// The fixer agent: when a change is rejected in review and its author does not
// answer, a fixer forks the author's Artifacts repository, repairs the change
// with a Workers AI model, and pushes. The push lands as another patchset of
// the same changeset, so the review history stays in one place.
//
// Artifacts has no write API, so the fixer pushes with real git protocol
// (isomorphic-git over an in-memory filesystem), exactly like any other agent.
//
// The same fixer repairs changesets agents pushed straight to Gitslice. It then
// reads the patchset through the Gitslice API and pushes its repair to
// refs/changes/<changeset> on Gitslice's own Git endpoint, with no fork.

import git from "isomorphic-git";
import http from "isomorphic-git/http/web";
import { createFsFromVolume, Volume } from "memfs";
import { diffTrees, readBlob } from "./artifacts";
import type { Env } from "./env";
import { Gitslice } from "./gitslice";
import { errorMessage, mapLimit } from "./land";
import { findChange, readNativeFiles, reviewNativeChange, type NativeHub } from "./native";
import { parseFix, type FileContext } from "./fixparse";
import { extractText, withTimeout } from "./review";
import type { Session } from "./types";

const MODEL = "@cf/openai/gpt-oss-120b";
const MAX_FILE_CHARS = 6000;

export interface FixerHub extends NativeHub {
  getSession(id: string): Promise<Session | null>;
  openFix(failingId: string): Promise<{ session: Session; token: string } | null>;
  patch(id: string, partial: Partial<Session>, signal?: Omit<Session["signals"][number], "at">, eventMessage?: string): Promise<Session | null>;
}

export async function fixSession(env: Env, hub: FixerHub, failingId: string): Promise<void> {
  const failing = await hub.getSession(failingId);
  if (!failing || failing.status !== "changes-requested" || !failing.lastCommit) return;
  const opened = await hub.openFix(failingId);
  if (!opened) return;
  const { session, token } = opened;
  const log = (stage: string, extra: Record<string, unknown> = {}) => console.log(JSON.stringify({ msg: "fixer", stage, session: session.id, ...extra }));
  try {
    log("forked");
    // What the author pushed, against the baseline it started from.
    const files = failing.native ? await readNativeChanges(env, failing) : await readChanges(env, failing);
    log("read", { files: files.map((f) => f.path) });
    const feedback = [failing.review?.summary, ...(failing.review?.concerns ?? [])].filter(Boolean).join("\n- ");
    const started = Date.now();
    const fix = await askModel(env.AI, failing, files, feedback);
    log("model", { summary: fix.summary });
    await hub.patch(session.id, {}, { kind: "info", message: `Repaired ${fix.files.length} file(s) with ${MODEL.replace("@cf/", "")} in ${((Date.now() - started) / 1000).toFixed(1)} s: ${fix.summary}` });

    const message = [
      `fix(${failing.agent}): ${fix.summary}`.slice(0, 100),
      "",
      `${failing.agent}'s change was rejected in review and the agent did not answer.`,
      `Review: ${failing.review?.summary ?? "changes requested"}`,
      "",
      `Fixes: ${failing.id}`,
      `Fixed-by: ${session.agent} (${MODEL.replace("@cf/", "")} on Workers AI)`,
      `Agent: ${session.agent}`,
      `Task: ${session.task}`,
    ].join("\n");
    if (failing.native) {
      // Update the author's changeset on Gitslice, then review the new patchset.
      await pushFix(session.remote, token, fix.files, message, session.agent, `refs/changes/${failing.changesetId}`);
      log("pushed");
      await reviewNativeChange(env, hub, { account: failing.account, slice: failing.slice }, failing.changesetId!);
      return;
    }
    await pushFix(session.remote, token, fix.files, message, session.agent);
    log("pushed");

    // Artifacts also emits repo.pushed; this only saves the wait.
    using repo = await env.ARTIFACTS.get(session.id);
    const [head] = await repo.log({ limit: 1 });
    if (head) await env.EVENTS.send({ kind: "land", session: session.id, commit: head.hash });
  } catch (err) {
    console.error(JSON.stringify({ msg: "fixer failed", session: failingId, error: errorMessage(err) }));
    await hub.patch(session.id, { status: "failed" }, { kind: "error", message: errorMessage(err) }, `fixer failed: ${errorMessage(err).slice(0, 100)}`);
  }
}

// readNativeChanges reads the failing patchset of a change pushed to Gitslice.
async function readNativeChanges(env: Env, failing: Session): Promise<FileContext[]> {
  const gitslice = new Gitslice(env.GITSLICE_API, env.GITSLICE_TOKEN);
  const slice = { account: failing.account, slice: failing.slice };
  const found = await findChange(gitslice, slice, failing.changesetId!);
  if (!found) throw new Error(`changeset ${failing.changesetId} not found`);
  const files = await readNativeFiles(gitslice, slice, found.cs, found.ps);
  return files.filter((f) => f.after !== null).map((f) => ({ path: f.path.replace(/^\//, ""), before: f.before, after: f.after! }));
}

async function readChanges(env: Env, failing: Session): Promise<FileContext[]> {
  const prefix = `${failing.account}/${failing.slice}/`;
  using repo = await env.ARTIFACTS.get(failing.id);
  const commit = await repo.readCommit(failing.lastCommit!);
  if (!commit) throw new Error(`commit ${failing.lastCommit} is not in ${failing.id}`);
  const changes = (await diffTrees(repo, failing.baseline.tree, commit.treeHash)).filter((c) => c.path.startsWith(prefix) && c.op === "upsert");
  const decoder = new TextDecoder();
  // Awaited here: the repo handle is disposed when this function returns.
  return await mapLimit(changes, 4, async (c) => ({
    path: c.path,
    before: c.baseHash ? decoder.decode(await readBlob(repo, c.baseHash)) : null,
    after: decoder.decode(await readBlob(repo, c.hash!)),
  }));
}

const INSTRUCTIONS = `You are a fixer agent on a shared codebase. Another coding agent pushed a change that the review agent rejected, and it did not answer.
Repair only what the review flagged, keep the change within its task, and keep everything else as the author wrote it.
Reply in exactly this format, and nothing else:

SUMMARY: one sentence on what you fixed
=== FILE: <path exactly as given> ===
<the complete corrected file>
=== END ===

Include one FILE block for every file you changed.`;

async function askModel(ai: Ai, failing: Session, files: FileContext[], feedback: string): Promise<{ summary: string; files: Array<{ path: string; content: string }> }> {
  const shown = files.map((f) => `--- ${f.path}\n${f.before === null ? "(new file)" : `BEFORE:\n${clip(f.before)}`}\nPUSHED:\n${clip(f.after)}`).join("\n\n");
  let prompt = [`Task of the author (${failing.agent}): ${failing.task}`, `Review feedback:\n- ${feedback}`, `Files:\n${shown}`].join("\n\n");
  let lastProblem = "";
  for (let attempt = 0; attempt < 2; attempt++) {
    const raw = await withTimeout(ai.run(MODEL as keyof AiModels, { instructions: INSTRUCTIONS, input: prompt, reasoning: { effort: "low" } } as never), 40_000, "fixer model");
    const parsed = parseFix(extractText(raw), files);
    if (typeof parsed !== "string") return parsed;
    lastProblem = parsed;
    prompt += `\n\nYour previous answer was rejected: ${parsed} Answer again in the required format.`;
  }
  throw new Error(`the model's fix was unusable: ${lastProblem}`);
}

// pushFix clones the fork (just its last commit), writes the repaired files,
// commits and pushes, over git smart HTTP.
async function pushFix(remote: string, token: string, files: Array<{ path: string; content: string }>, message: string, name: string, remoteRef = "main"): Promise<void> {
  const fs = createFsFromVolume(new Volume());
  const headers = { Authorization: `Bearer ${token}` };
  const dir = "/work";
  await git.clone({ fs, http, dir, url: remote, ref: "main", singleBranch: true, depth: 1, headers });
  for (const f of files) {
    await fs.promises.writeFile(`${dir}/${f.path}`, f.content);
    await git.add({ fs, dir, filepath: f.path });
  }
  await git.commit({ fs, dir, message, author: { name, email: `${name}@agents.gitslice.io` } });
  const result = await git.push({ fs, http, dir, remote: "origin", ref: "main", remoteRef, headers });
  if (!result.ok) throw new Error(`push rejected: ${result.error ?? JSON.stringify(result.refs)}`);
}

function clip(text: string): string {
  return text.length > MAX_FILE_CHARS ? text.slice(0, MAX_FILE_CHARS) + "\n… (truncated)" : text;
}
