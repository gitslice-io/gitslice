import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DiffViewer, type DiffViewerFileState } from "./DiffViewer";
import { diffFileId, parseDiff } from "./parseDiff";

describe("DiffViewer", () => {
  afterEach(() => {
    cleanup();
    window.localStorage.clear();
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
