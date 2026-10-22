import type { BlameFileResponse, Commit } from "../../api/types";

// BlameLine is what the gutter shows for one line of the file.
export interface BlameLine {
  // The commit that last changed the line, when the blame names it.
  commit?: Commit;
  commitId: string;
  // The first line of a run changed by one commit: it carries the details.
  first: boolean;
  // Which run this is, counting from the top, for banding.
  run: number;
  // The line's place in its run (0 for the first) and the run's length.
  offset: number;
  length: number;
  // The commit's place among the commits the blame names, oldest first: 1 is
  // the oldest change still in the file, total the newest.
  order: number;
  total: number;
}

// blameLines expands a blame's runs into one entry per line, 0-based. Lines no
// run covers (a malformed answer) come back with an empty commit id rather
// than being dropped, so the gutter stays aligned with the code.
export function blameLines(blame: BlameFileResponse, lineCount: number): BlameLine[] {
  const commits = new Map<string, Commit>();
  for (const commit of blame.commits ?? []) {
    if (commit.id) {
      commits.set(commit.id, commit);
    }
  }
  const order = changeOrder(blame);
  const total = order.size;
  const lines: BlameLine[] = Array.from({ length: lineCount }, () => ({
    commitId: "",
    first: false,
    length: 0,
    offset: 0,
    order: 0,
    run: 0,
    total
  }));
  let run = 0;
  for (const range of blame.ranges ?? []) {
    const start = (range.startLine ?? 1) - 1;
    const count = range.lineCount ?? 0;
    const commitId = range.commitId ?? "";
    for (let offset = 0; offset < count; offset++) {
      const line = lines[start + offset];
      if (!line) {
        continue;
      }
      line.commitId = commitId;
      line.commit = commits.get(commitId);
      line.first = offset === 0;
      line.run = run;
      line.offset = offset;
      line.length = count;
      line.order = order.get(commitId) ?? 0;
    }
    run++;
  }
  return lines;
}

// changeOrder numbers the commits a blame's runs name by when they were made,
// oldest first, from 1. A commit with no time counts as older than any with
// one; ties keep the order the runs name them in.
export function changeOrder(blame: BlameFileResponse): Map<string, number> {
  const created = new Map<string, number>();
  for (const commit of blame.commits ?? []) {
    if (commit.id) {
      const time = Date.parse(commit.createdAt ?? "");
      created.set(commit.id, Number.isNaN(time) ? Number.NEGATIVE_INFINITY : time);
    }
  }
  const seen = new Set<string>();
  for (const range of blame.ranges ?? []) {
    if (range.commitId) {
      seen.add(range.commitId);
    }
  }
  const ids = [...seen];
  const sorted = ids
    .map((id, index) => ({ id, index, time: created.get(id) ?? Number.NEGATIVE_INFINITY }))
    .sort((a, b) => (a.time === b.time ? a.index - b.index : a.time < b.time ? -1 : 1));
  return new Map(sorted.map((entry, index) => [entry.id, index + 1]));
}

// BlameBand is a stretch of lines, 0-based and inclusive, that one commit
// changed and that sit together in the file.
export interface BlameBand {
  start: number;
  end: number;
}

// commitBands is where a commit's lines are in the file, as runs, so they can
// be marked on the code: a commit that changed three separate places has
// three bands.
export function commitBands(lines: BlameLine[], commitId: string): BlameBand[] {
  const bands: BlameBand[] = [];
  if (!commitId) {
    return bands;
  }
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].commitId !== commitId) {
      continue;
    }
    const open = bands[bands.length - 1];
    if (open && open.end === i - 1 && !lines[i].first) {
      open.end = i;
    } else {
      bands.push({ end: i, start: i });
    }
  }
  return bands;
}

// codeLineCount is how many lines a blame counts in text: a final newline ends
// the last line, it does not start another.
export function codeLineCount(code: string): number {
  if (!code) {
    return 0;
  }
  const lines = code.split(/\r\n|\r|\n/).length;
  return /(\r\n|\r|\n)$/.test(code) ? lines - 1 : lines;
}
