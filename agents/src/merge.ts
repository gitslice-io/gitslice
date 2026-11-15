// Line diffs and three-way merges for agent changes that touch the same file.
//
// Gitslice validates submits per path. When another agent landed a change to a
// file this agent also edited, the submit fails with a stale path base. Most
// of those races touch different parts of the file, so the bridge merges them
// line by line (diff3) and resubmits; only real overlaps go back to the agent.

export type MergeResult =
  | { clean: true; text: string }
  | { clean: false; conflicts: ConflictHunk[] };

export interface ConflictHunk {
  base: string[];
  ours: string[];
  theirs: string[];
}

// Hard bounds so Myers' diff never OOMs a 128 MiB Worker isolate on generated
// or huge files.
export const MAX_DIFF_LINES = 4_000;
export const MAX_EDIT_DISTANCE = 1_000;
const MAX_CONFLICT_PREVIEW_LINES = 80;

export interface LcsOptions {
  maxLines?: number;
  maxEditDistance?: number;
}

// splitLines keeps each line's terminator so joining is lossless.
export function splitLines(text: string): string[] {
  if (text === "") return [];
  return text.match(/[^\n]*\n|[^\n]+$/g) ?? [];
}

function lcsPairsBounded(
  a: string[],
  b: string[],
  maxLines = MAX_DIFF_LINES,
  maxEditDistance = MAX_EDIT_DISTANCE,
): { pairs: Array<[number, number]>; complete: boolean } {
  const nTotal = a.length;
  const mTotal = b.length;

  let prefix = 0;
  while (prefix < nTotal && prefix < mTotal && a[prefix] === b[prefix]) {
    prefix++;
  }
  let suffix = 0;
  while (
    suffix < nTotal - prefix &&
    suffix < mTotal - prefix &&
    a[nTotal - 1 - suffix] === b[mTotal - 1 - suffix]
  ) {
    suffix++;
  }

  const prefixPairs: Array<[number, number]> = [];
  for (let i = 0; i < prefix; i++) prefixPairs.push([i, i]);

  const suffixPairs: Array<[number, number]> = [];
  for (let i = suffix - 1; i >= 0; i--) {
    suffixPairs.push([nTotal - 1 - i, mTotal - 1 - i]);
  }

  const n = nTotal - prefix - suffix;
  const m = mTotal - prefix - suffix;
  if (n === 0 || m === 0) {
    return { pairs: [...prefixPairs, ...suffixPairs], complete: true };
  }
  if (n > maxLines || m > maxLines || Math.abs(n - m) > maxEditDistance) {
    return { pairs: [...prefixPairs, ...suffixPairs], complete: false };
  }

  const limit = Math.min(n + m, maxEditDistance);
  const offset = limit + 1;
  const v = new Int32Array(2 * limit + 3);
  // Store only the active diagonal window `[-d, d]` per step (2d + 1 ints)
  // rather than the full 2*(n+m)+3 buffer.
  const trace: Int32Array[] = [];
  let found = false;
  for (let d = 0; d <= limit && !found; d++) {
    trace.push(v.slice(offset - d, offset + d + 1));
    for (let k = -d; k <= d; k += 2) {
      let x: number;
      if (k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1])) {
        x = v[offset + k + 1];
      } else {
        x = v[offset + k - 1] + 1;
      }
      let y = x - k;
      while (x < n && y < m && a[prefix + x] === b[prefix + y]) {
        x++;
        y++;
      }
      v[offset + k] = x;
      if (x >= n && y >= m) {
        found = true;
        break;
      }
    }
  }
  if (!found) {
    return { pairs: [...prefixPairs, ...suffixPairs], complete: false };
  }

  // Walk the compact trace backwards to recover the matched diagonals.
  const midPairs: Array<[number, number]> = [];
  let x = n;
  let y = m;
  for (let d = trace.length - 1; d >= 0 && (x > 0 || y > 0); d--) {
    const vd = trace[d]; // length 2*d + 1, where index `k + d` holds `v[offset + k]`
    const at = (k: number): number => (k >= -d && k <= d ? vd[k + d] : 0);
    const k = x - y;
    let prevK: number;
    if (k === -d || (k !== d && at(k - 1) < at(k + 1))) {
      prevK = k + 1;
    } else {
      prevK = k - 1;
    }
    const prevX = at(prevK);
    const prevY = prevX - prevK;
    while (x > prevX && y > prevY) {
      x--;
      y--;
      midPairs.push([prefix + x, prefix + y]);
    }
    x = prevX;
    y = prevY;
  }
  midPairs.reverse();
  return { pairs: [...prefixPairs, ...midPairs, ...suffixPairs], complete: true };
}

// lcsPairs returns matching (a, b) index pairs of a longest common
// subsequence, in increasing order, using bounded Myers' O(ND) algorithm. When
// inputs exceed line/edit-distance bounds, it returns the matched common
// prefix/suffix without exhausting memory.
export function lcsPairs(a: string[], b: string[], opts?: LcsOptions): Array<[number, number]> {
  return lcsPairsBounded(a, b, opts?.maxLines, opts?.maxEditDistance).pairs;
}

function sameLines(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((line, i) => line === b[i]);
}

// merge3 merges ours and theirs, both derived from base. Chunks changed on
// only one side take that side; identical changes merge; anything else (or
// diffs exceeding Worker memory bounds) falls back cleanly to conflict.
export function merge3(base: string, ours: string, theirs: string, opts?: LcsOptions): MergeResult {
  if (ours === theirs) return { clean: true, text: ours };
  if (ours === base) return { clean: true, text: theirs };
  if (theirs === base) return { clean: true, text: ours };

  const b = splitLines(base);
  const a = splitLines(ours);
  const t = splitLines(theirs);
  const lcsA = lcsPairsBounded(b, a, opts?.maxLines, opts?.maxEditDistance);
  const lcsT = lcsPairsBounded(b, t, opts?.maxLines, opts?.maxEditDistance);
  if (!lcsA.complete || !lcsT.complete) {
    return {
      clean: false,
      conflicts: [
        {
          base: b.slice(0, MAX_CONFLICT_PREVIEW_LINES),
          ours: a.slice(0, MAX_CONFLICT_PREVIEW_LINES),
          theirs: t.slice(0, MAX_CONFLICT_PREVIEW_LINES),
        },
      ],
    };
  }
  const aOf = new Int32Array(b.length).fill(-1);
  const tOf = new Int32Array(b.length).fill(-1);
  for (const [bi, ai] of lcsA.pairs) aOf[bi] = ai;
  for (const [bi, ti] of lcsT.pairs) tOf[bi] = ti;

  const out: string[] = [];
  const conflicts: ConflictHunk[] = [];
  let ib = 0;
  let ia = 0;
  let it = 0;
  const resolve = (bc: string[], ac: string[], tc: string[]) => {
    if (sameLines(ac, tc)) out.push(...ac);
    else if (sameLines(ac, bc)) out.push(...tc);
    else if (sameLines(tc, bc)) out.push(...ac);
    else conflicts.push({ base: bc, ours: ac, theirs: tc });
  };
  for (let k = 0; k < b.length; k++) {
    if (aOf[k] < ia || tOf[k] < it) continue;
    // k is a sync point: base line k is kept on both sides.
    resolve(b.slice(ib, k), a.slice(ia, aOf[k]), t.slice(it, tOf[k]));
    out.push(b[k]);
    ib = k + 1;
    ia = aOf[k] + 1;
    it = tOf[k] + 1;
  }
  resolve(b.slice(ib), a.slice(ia), t.slice(it));
  if (conflicts.length > 0) return { clean: false, conflicts };
  return { clean: true, text: out.join("") };
}

// unifiedDiff renders a compact unified diff for review prompts and signals.
export function unifiedDiff(path: string, before: string, after: string, context = 3): string {
  const a = splitLines(before);
  const b = splitLines(after);
  const pairs = lcsPairs(a, b);
  type Op = { kind: " " | "-" | "+"; line: string; ai: number; bi: number };
  const ops: Op[] = [];
  let ai = 0;
  let bi = 0;
  for (const [pa, pb] of [...pairs, [a.length, b.length] as [number, number]]) {
    while (ai < pa) ops.push({ kind: "-", line: a[ai], ai: ai++, bi });
    while (bi < pb) ops.push({ kind: "+", line: b[bi], ai, bi: bi++ });
    if (pa < a.length && pb < b.length) {
      ops.push({ kind: " ", line: a[pa], ai: ai++, bi: bi++ });
    }
  }
  const lines: string[] = [`--- a/${path}`, `+++ b/${path}`];
  const changes = ops.flatMap((op, i) => (op.kind === " " ? [] : [i]));
  let c = 0;
  while (c < changes.length) {
    // Group changes whose gaps fit inside the shared context.
    let last = changes[c];
    let next = c + 1;
    while (next < changes.length && changes[next] - last <= context * 2) {
      last = changes[next];
      next++;
    }
    const hunk = ops.slice(Math.max(0, changes[c] - context), Math.min(ops.length, last + context + 1));
    const aLen = hunk.filter((o) => o.kind !== "+").length;
    const bLen = hunk.filter((o) => o.kind !== "-").length;
    lines.push(`@@ -${hunk[0].ai + 1},${aLen} +${hunk[0].bi + 1},${bLen} @@`);
    for (const op of hunk) lines.push(op.kind + op.line.replace(/\n$/, ""));
    c = next;
  }
  return lines.join("\n");
}

export function isText(data: Uint8Array): boolean {
  if (data.includes(0)) return false;
  try {
    new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(data);
    return true;
  } catch {
    return false;
  }
}
