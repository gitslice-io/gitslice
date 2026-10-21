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
  const lines: BlameLine[] = Array.from({ length: lineCount }, () => ({ commitId: "", first: false, run: 0 }));
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
    }
    run++;
  }
  return lines;
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
