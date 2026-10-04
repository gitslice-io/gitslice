import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const renderMermaid = vi.fn();
const renderDot = vi.fn();

vi.mock("../../lib/diagram", async () => {
  const actual = await vi.importActual<typeof import("../../lib/diagram")>("../../lib/diagram");
  return { ...actual, renderDot: (...args: unknown[]) => renderDot(...args), renderMermaid: (...args: unknown[]) => renderMermaid(...args) };
});

import { DiagramViewer, diagramKindFromPath } from "./DiagramViewer";
import { MarkdownViewer } from "./MarkdownViewer";

beforeEach(() => {
  renderMermaid.mockReset();
  renderDot.mockReset();
});
afterEach(cleanup);

const SVG = '<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>';

describe("DiagramViewer", () => {
  it("picks the engine from the extension", () => {
    expect(diagramKindFromPath("/a/flow.mmd")).toBe("mermaid");
    expect(diagramKindFromPath("/a/flow.mermaid")).toBe("mermaid");
    expect(diagramKindFromPath("/a/graph.dot")).toBe("dot");
    expect(diagramKindFromPath("/a/graph.GV")).toBe("dot");
  });

  it("shows a Mermaid diagram as an image, not as markup", async () => {
    renderMermaid.mockResolvedValue(SVG);
    render(<DiagramViewer path="/docs/flow.mmd" source="graph TD; A-->B" />);

    const image = await screen.findByRole("img", { name: "Diagram of flow.mmd" });
    expect(image.getAttribute("src")).toMatch(/^data:image\/svg\+xml;base64,/);
    expect(document.querySelector("svg")).toBeNull();
    expect(renderMermaid).toHaveBeenCalledWith("graph TD; A-->B", false);
  });

  it("renders DOT with Graphviz", async () => {
    renderDot.mockResolvedValue(SVG);
    render(<DiagramViewer path="/docs/g.dot" source="digraph { a -> b }" />);

    await screen.findByRole("img", { name: "Diagram of g.dot" });
    expect(renderDot).toHaveBeenCalledWith("digraph { a -> b }");
    expect(renderMermaid).not.toHaveBeenCalled();
  });

  it("says why a diagram failed", async () => {
    renderMermaid.mockRejectedValue(new Error("Parse error on line 1"));
    render(<DiagramViewer path="/docs/bad.mmd" source="nonsense" />);

    expect(await screen.findByRole("alert")).toHaveTextContent("Parse error on line 1");
  });
});

describe("Markdown with Mermaid blocks", () => {
  it("replaces a mermaid fence with its diagram and leaves other code alone", async () => {
    renderMermaid.mockResolvedValue(SVG);
    const source = "# Doc\n\n```mermaid\ngraph TD; A-->B\n```\n\n```go\nfunc main() {}\n```\n";
    render(<MarkdownViewer source={source} />);

    expect(await screen.findByRole("img", { name: "Mermaid diagram" })).toBeInTheDocument();
    expect(renderMermaid).toHaveBeenCalledTimes(1);
    expect(screen.getByText("func main() {}")).toBeInTheDocument();
  });

  it("keeps the source when the diagram does not render", async () => {
    renderMermaid.mockRejectedValue(new Error("bad"));
    render(<MarkdownViewer source={"```mermaid\nnot a diagram\n```\n"} />);

    await waitFor(() => expect(renderMermaid).toHaveBeenCalled());
    expect(screen.getByText("not a diagram")).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
  });
});
