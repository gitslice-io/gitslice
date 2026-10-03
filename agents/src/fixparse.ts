// Reading and checking a fixer's reply. Pure, so it is tested without a Worker.

export interface FileContext {
  path: string; // repository path, e.g. demo/store/src/i18n/messages.ga.json
  before: string | null;
  after: string;
}

// parseFix reads the model's reply and checks it before anything is pushed:
// paths must be files the author changed, and JSON must parse.
export function parseFix(text: string, files: FileContext[]): { summary: string; files: Array<{ path: string; content: string }> } | string {
  const summary = /^SUMMARY:\s*(.+)$/m.exec(text)?.[1]?.trim() ?? "";
  const blocks = [...text.matchAll(/^=== FILE: (.+?) ===\n([\s\S]*?)\n=== END ===/gm)];
  if (!summary) return "there was no SUMMARY line.";
  if (blocks.length === 0) return "there was no FILE block.";
  const known = new Set(files.map((f) => f.path));
  const out: Array<{ path: string; content: string }> = [];
  for (const [, rawPath, body] of blocks) {
    const path = rawPath.trim().replace(/^\/+/, "");
    if (!known.has(path)) return `${path} is not one of the changed files.`;
    const content = body.endsWith("\n") ? body : body + "\n";
    if (path.endsWith(".json")) {
      try {
        JSON.parse(content);
      } catch (err) {
        return `${path} is still not valid JSON (${String(err)}).`;
      }
    }
    out.push({ path, content });
  }
  return { summary: summary.slice(0, 160), files: out };
}
