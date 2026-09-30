import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DiffViewer, type DiffViewerFileState } from "./DiffViewer";
import { diffFileId, parseDiff, type DiffFile } from "./parseDiff";

describe("DiffViewer", () => {
  afterEach(() => {
    cleanup();
    window.localStorage.clear();
    vi.unstubAllGlobals();
  });

  it("renders binary and too-large server stubs as meta rows in both views", () => {
    const diff = [
      "diff --git a/image.png b/image.png",
      "Binary files a/image.png and b/image.png differ",
      "diff --git a/generated.txt b/generated.txt",
      "Diff too large to render: generated.txt (8.4 MB)",
      ""
    ].join("\n");

    render(
      <DiffViewer
        diffResponse={{
          changedPaths: ["generated.txt", "image.png"],
          diff
        }}
        error={null}
        isError={false}
        isLoading={false}
      />
    );

    expect(
      screen.getByText("Binary files a/image.png and b/image.png differ")
    ).toBeInTheDocument();
    expect(
      screen.getByText("Diff too large to render: generated.txt (8.4 MB)")
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "split" }));

    expect(
      screen.getByText("Binary files a/image.png and b/image.png differ")
    ).toBeInTheDocument();
    expect(
      screen.getByText("Diff too large to render: generated.txt (8.4 MB)")
    ).toBeInTheDocument();
  });

  it("guards diffs over 5,000 lines until Show diff is clicked", () => {
    const contextLines = Array.from(
      { length: 5001 },
      (_, index) => ` context ${index + 1}`
    );
    const diff = [
      "diff --git a/large.txt b/large.txt",
      "--- a/large.txt",
      "+++ b/large.txt",
      "@@ -1,5001 +1,5002 @@",
      ...contextLines,
      "+tail-marker",
      ""
    ].join("\n");

    render(
      <DiffViewer
        diffResponse={{ changedPaths: ["large.txt"], diff }}
        error={null}
        isError={false}
        isLoading={false}
      />
    );

    expect(screen.getByText("Large diff (5006 lines)")).toBeInTheDocument();
    expect(screen.queryByText("+tail-marker")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Show diff" }));

    expect(screen.getByText("+tail-marker")).toBeInTheDocument();
  });

  it("hides redundant git headers while keeping other unified meta lines", () => {
    const diff = [
      "diff --git a/example.txt b/example.txt",
      "old mode 100644",
      "index 1111111..2222222 100755",
      "--- a/example.txt",
      "+++ b/example.txt",
      "@@ -1 +1 @@",
      "-before",
      "+after",
      ""
    ].join("\n");

    render(
      <DiffViewer
        diffResponse={{ changedPaths: ["example.txt"], diff }}
        error={null}
        isError={false}
        isLoading={false}
      />
    );

    expect(screen.queryByText("diff --git a/example.txt b/example.txt")).not.toBeInTheDocument();
    expect(screen.queryByText("index 1111111..2222222 100755")).not.toBeInTheDocument();
    expect(screen.queryByText("--- a/example.txt")).not.toBeInTheDocument();
    expect(screen.queryByText("+++ b/example.txt")).not.toBeInTheDocument();
    expect(screen.getByText("old mode 100644")).toBeInTheDocument();
  });

  it("says a pure rename has no line changes instead of rendering an empty block", () => {
    const diff = [
      "diff --git a/old.txt b/new.txt",
      "similarity index 100%",
      "rename from old.txt",
      "rename to new.txt",
      ""
    ].join("\n");

    render(
      <DiffViewer
        diffResponse={{ changedPaths: ["new.txt"], diff }}
        error={null}
        isError={false}
        isLoading={false}
      />
    );

    expect(screen.getByText("No line changes in this file.")).toBeInTheDocument();
    expect(screen.queryByText("rename from old.txt")).not.toBeInTheDocument();
  });

  it("hides redundant git header rows in split view", () => {
    const file: DiffFile = {
      additions: 1,
      changeKind: "modified",
      deletions: 1,
      id: diffFileId("example.txt"),
      lines: [],
      path: "example.txt",
      rows: [
        { hunkText: "diff --git a/example.txt b/example.txt", kind: "meta" },
        { hunkText: "old mode 100644", kind: "meta" },
        { hunkText: "index 1111111..2222222 100755", kind: "meta" },
        { hunkText: "@@ -1 +1 @@", kind: "hunk" },
        {
          kind: "replace",
          left: { content: "before", kind: "del", oldNumber: 1, text: "-before" },
          right: { content: "after", kind: "add", newNumber: 1, text: "+after" }
        }
      ]
    };

    render(
      <DiffViewer
        error={null}
        fileStates={[{ file, path: file.path, status: "loaded" }]}
        isError={false}
        isLoading={false}
      />
    );
    fireEvent.click(screen.getByRole("button", { name: "split" }));

    expect(screen.queryByText("diff --git a/example.txt b/example.txt")).not.toBeInTheDocument();
    expect(screen.queryByText("index 1111111..2222222 100755")).not.toBeInTheDocument();
    expect(screen.getByText("old mode 100644")).toBeInTheDocument();
  });

  it("marks totals as partial and omits counts for unloaded file diffs", () => {
    const loadedPath = "loaded.ts";
    const pendingPath = "pending.ts";
    const loadedFile = parseDiff(
      [
        "diff --git a/loaded.ts b/loaded.ts",
        "--- a/loaded.ts",
        "+++ b/loaded.ts",
        "@@ -1 +1 @@",
        "-before",
        "+after",
        ""
      ].join("\n"),
      [loadedPath]
    )[0];

    render(
      <DiffViewer
        error={null}
        fileStates={[
          { file: loadedFile, path: loadedPath, status: "loaded" },
          { path: pendingPath, status: "pending" }
        ]}
        isError={false}
        isLoading={false}
      />
    );

    const totals = screen.getByText("so far").parentElement;
    expect(totals).toHaveTextContent("+1");
    expect(totals).toHaveTextContent("-1");
    const loadedRow = screen
      .getAllByRole("button", { name: /loaded\.ts/i })
      .find(
        (button) =>
          !button.getAttribute("aria-label")?.startsWith("Open file picker")
      );
    expect(loadedRow).toHaveTextContent("+1");
    const pendingRow = screen.getByRole("button", { name: /pending\.ts/i });
    expect(pendingRow).not.toHaveTextContent(/\+0/);
    expect(pendingRow).not.toHaveTextContent(/-0/);

    // The picker sheet watches the desktop breakpoint; jsdom has no matchMedia.
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {}
    }));
    fireEvent.click(screen.getByRole("button", { name: /Open file picker/i }));
    expect(within(screen.getByRole("dialog")).getByText("so far")).toBeInTheDocument();
  });

  it("keeps a path-backed panel id stable while its lazy diff loads", () => {
    const path = "src/parser.ts";
    const pending: DiffViewerFileState[] = [
      { changeKind: "modified", path, status: "pending" }
    ];
    const { rerender } = render(
      <DiffViewer
        error={null}
        fileStates={pending}
        isError={false}
        isLoading={false}
      />
    );
    const panel = screen.getByRole("article");

    expect(panel).toHaveAttribute("id", diffFileId(path));
    expect(
      screen.getByText("Diff loads as this file nears the viewport.")
    ).toBeInTheDocument();

    const file = parseDiff(
      [
        "diff --git a/src/parser.ts b/src/parser.ts",
        "--- a/src/parser.ts",
        "+++ b/src/parser.ts",
        "@@ -1 +1 @@",
        "-old",
        "+new",
        ""
      ].join("\n"),
      [path]
    )[0];
    rerender(
      <DiffViewer
        error={null}
        fileStates={[{ file, path, status: "loaded" }]}
        isError={false}
        isLoading={false}
      />
    );

    expect(screen.getByRole("article")).toHaveAttribute("id", diffFileId(path));
    expect(screen.getByText("+new")).toBeInTheDocument();
  });

  it("retries a failed per-file diff from its inline error body", () => {
    const onFileRetry = vi.fn();
    const path = "src/broken.ts";

    render(
      <DiffViewer
        error={null}
        fileStates={[
          {
            error: new Error("Unable to load this file."),
            path,
            status: "error"
          }
        ]}
        isError={false}
        isLoading={false}
        onFileRetry={onFileRetry}
      />
    );

    expect(screen.getByText("Unable to load this file.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onFileRetry).toHaveBeenCalledWith(path);
  });
});
