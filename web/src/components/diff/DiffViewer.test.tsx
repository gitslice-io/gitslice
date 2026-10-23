import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { hydrateRoot } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DiffViewer, type DiffViewerFileState } from "./DiffViewer";
import { diffFileId, parseDiff } from "./parseDiff";

describe("DiffViewer", () => {
  afterEach(() => {
    cleanup();
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

  it("wraps long lines to the panel width instead of scrolling sideways", () => {
    const long = `const x = "${"a".repeat(400)}";`;
    const diff = [
      "diff --git a/long.ts b/long.ts",
      "--- a/long.ts",
      "+++ b/long.ts",
      "@@ -1 +1 @@",
      "-const x = 1;",
      `+${long}`,
      ""
    ].join("\n");

    const { container } = render(
      <DiffViewer
        diffResponse={{ changedPaths: ["long.ts"], diff }}
        error={null}
        isError={false}
        isLoading={false}
      />
    );

    const expectWrapped = (text: string) => {
      expect(container.querySelector(".overflow-x-auto")).toBeNull();
      expect(container.querySelector(".min-w-max, .w-max")).toBeNull();
      expect(screen.getByText(text)).toHaveClass(
        "whitespace-pre-wrap",
        "[overflow-wrap:anywhere]"
      );
    };

    expectWrapped(`+${long}`);
    fireEvent.click(screen.getByRole("button", { name: "split" }));
    expectWrapped(long);
  });

  it("renders the view mode it is given, from the server render on, and reports toggles", async () => {
    const diff = [
      "diff --git a/a.ts b/a.ts",
      "--- a/a.ts",
      "+++ b/a.ts",
      "@@ -1 +1 @@",
      "-const a = 1;",
      "+const a = 2;",
      ""
    ].join("\n");
    const onViewModeChange = vi.fn();
    const viewer = (
      <DiffViewer
        diffResponse={{ changedPaths: ["a.ts"], diff }}
        error={null}
        isError={false}
        isLoading={false}
        onViewModeChange={onViewModeChange}
        viewMode="split"
      />
    );
    const container = document.createElement("div");
    container.innerHTML = renderToString(viewer);
    document.body.appendChild(container);
    expect(within(container).getByRole("button", { name: "split" })).toHaveAttribute("aria-pressed", "true");

    const onRecoverableError = vi.fn();
    const root = await act(async () => hydrateRoot(container, viewer, { onRecoverableError }));

    expect(onRecoverableError).not.toHaveBeenCalled();
    expect(within(container).getByRole("button", { name: "split" })).toHaveAttribute("aria-pressed", "true");
    // Split rows show the line without its +/- marker.
    expect(within(container).getByText("const a = 2;")).toBeInTheDocument();
    expect(within(container).queryByText("+const a = 2;")).toBeNull();

    // The owner of the mode decides; the viewer only reports the choice.
    fireEvent.click(within(container).getByRole("button", { name: "unified" }));
    expect(onViewModeChange).toHaveBeenCalledWith("unified");
    expect(within(container).getByRole("button", { name: "split" })).toHaveAttribute("aria-pressed", "true");

    act(() => root.unmount());
    container.remove();
  });

  it("offers each file in full, with its change kind and old path", () => {
    const diff = [
      "diff --git a/old.go b/new.go",
      "similarity index 90%",
      "rename from old.go",
      "rename to new.go",
      "--- a/old.go",
      "+++ b/new.go",
      "@@ -1 +1 @@",
      "-package old",
      "+package new",
      ""
    ].join("\n");
    const onViewFile = vi.fn();

    render(
      <DiffViewer
        diffResponse={{ changedPaths: ["new.go"], diff }}
        error={null}
        isError={false}
        isLoading={false}
        onViewFile={onViewFile}
      />
    );

    fireEvent.click(screen.getByRole("button", { name: "View file" }));
    expect(onViewFile).toHaveBeenCalledWith(
      expect.objectContaining({ changeKind: "renamed", oldPath: "old.go", path: "new.go" })
    );
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
