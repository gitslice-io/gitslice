import { describe, expect, it } from "vitest";

// Every component source, as text.
const sources = import.meta.glob<string>(
  ["./**/*.tsx", "!./**/*.test.tsx", "!./gen/**"],
  { eager: true, import: "default", query: "?raw" }
);

// Markdown rendered with Tailwind's typography plugin keeps its light-mode
// colors in dark mode (dark headings, quotes and bold text on a dark page,
// almost unreadable) unless the container also has dark:prose-invert.
describe("markdown containers", () => {
  it("find the component sources", () => {
    expect(Object.keys(sources).some((file) => file.endsWith("MarkdownViewer.tsx"))).toBe(true);
  });

  it("invert their typography in dark mode", () => {
    const missing: string[] = [];
    for (const [file, text] of Object.entries(sources)) {
      text.split("\n").forEach((line, index) => {
        if (/className=.*"prose\s/.test(line) && !line.includes("dark:prose-invert")) {
          missing.push(`${file}:${index + 1}`);
        }
      });
    }
    expect(missing).toEqual([]);
  });
});
