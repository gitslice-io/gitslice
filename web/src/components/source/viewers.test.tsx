import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { JsonLinesViewer, parseJsonLines, recordsToTable } from "./JsonLinesViewer";
import { JsonViewer } from "./JsonViewer";
import { LogViewer, logLevel } from "./LogViewer";
import { NotebookViewer } from "./NotebookViewer";
import { StructuredViewer, parseStructured } from "./StructuredViewer";
import { SourceCodeViewer } from "./SourceCodeViewer";
import { binaryKindFromPath, previewKindFromPath } from "./sourceUtils";

afterEach(cleanup);

describe("file kinds", () => {
  it("picks a preview by extension, ignoring case", () => {
    expect(previewKindFromPath("/a/data.CSV")).toBe("table");
    expect(previewKindFromPath("/a/x.json")).toBe("json");
    expect(previewKindFromPath("/a/x.ndjson")).toBe("jsonl");
    expect(previewKindFromPath("/a/ci.yml")).toBe("structured");
    expect(previewKindFromPath("/a/Cargo.toml")).toBe("structured");
    expect(previewKindFromPath("/a/n.ipynb")).toBe("notebook");
    expect(previewKindFromPath("/a/flow.mmd")).toBe("diagram");
    expect(previewKindFromPath("/a/graph.gv")).toBe("diagram");
    expect(previewKindFromPath("/a/server.log")).toBe("log");
    expect(previewKindFromPath("/a/README.md")).toBe("markdown");
    expect(previewKindFromPath("/a/main.go")).toBeNull();
    expect(previewKindFromPath("/a/json")).toBeNull();
  });

  it("recognizes files that are shown from their bytes", () => {
    expect(binaryKindFromPath("/a/doc.PDF")).toBe("pdf");
    expect(binaryKindFromPath("/a/clip.mp4")).toBe("video");
    expect(binaryKindFromPath("/a/song.mp3")).toBe("audio");
    expect(binaryKindFromPath("/a/book.xlsx")).toBe("spreadsheet");
    expect(binaryKindFromPath("/a/t.parquet")).toBe("parquet");
    expect(binaryKindFromPath("/a/main.go")).toBeNull();
  });
});

describe("JsonViewer", () => {
  it("shows a document as a tree that opens and closes", () => {
    render(<JsonViewer source={'{"name":"gitslice","deep":{"inner":{"leaf":1}},"list":[1,2,3]}'} />);

    expect(screen.getByText('"name"')).toBeInTheDocument();
    expect(screen.getByText('"gitslice"')).toBeInTheDocument();
    // Depth 2 is open by default; the leaf below it is not.
    expect(screen.getByText('"inner"')).toBeInTheDocument();
    expect(screen.queryByText('"leaf"')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Collapse root" }));
    expect(screen.queryByText('"name"')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Expand all" }));
    expect(screen.getByText('"leaf"')).toBeInTheDocument();
  });

  it("pages through long arrays", () => {
    render(<JsonViewer source={JSON.stringify({ items: Array.from({ length: 250 }, (_, i) => i) })} />);

    expect(screen.getByRole("button", { name: /Show 150 more of 150/ })).toBeInTheDocument();
  });

  it("explains a file that is not JSON", () => {
    render(<JsonViewer source={"{not json"} />);

    expect(screen.getByRole("alert")).toHaveTextContent("This is not valid JSON");
  });

  it("says when the file is empty", () => {
    render(<JsonViewer source={"  \n"} />);

    expect(screen.getByText("This file is empty.")).toBeInTheDocument();
  });

  it("shows markup in strings as text", () => {
    render(<JsonViewer source={'{"x":"<img src=x onerror=alert(1)>"}'} />);

    expect(document.querySelector("img")).toBeNull();
  });
});

describe("JsonLinesViewer", () => {
  const source = '{"ts":1,"level":"info","msg":"start"}\n{"ts":2,"level":"error","msg":"boom","extra":{"a":1}}\nnot json\n';

  it("parses lines and reports the ones that fail", () => {
    const parsed = parseJsonLines(source);
    expect(parsed.records).toHaveLength(2);
    expect(parsed.bad).toEqual([3]);
  });

  it("builds one column per key", () => {
    const table = recordsToTable(parseJsonLines(source).records);
    expect(table.header).toEqual(["ts", "level", "msg", "extra"]);
    expect(table.rows[1]).toEqual(["2", "error", "boom", '{"a":1}']);
    expect(table.rows[0][3]).toBe("");
  });

  it("puts values that are not objects in a value column", () => {
    const table = recordsToTable([1, "two", { a: 3 }]);
    expect(table.header).toEqual(["value", "a"]);
    expect(table.rows).toEqual([["1", ""], ["two", ""], ["", "3"]]);
  });

  it("shows a table, and the records as trees", () => {
    render(<JsonLinesViewer source={source} />);

    expect(screen.getByRole("columnheader", { name: "msg" })).toBeInTheDocument();
    expect(screen.getByText(/1 line could not be parsed \(first: line 3\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "records" }));
    expect(screen.getByRole("button", { name: "Expand all" })).toBeInTheDocument();
  });
});

describe("StructuredViewer", () => {
  it("parses YAML, including several documents", async () => {
    expect(await parseStructured("yaml", "a: 1\nb: [x, y]\n")).toEqual({ a: 1, b: ["x", "y"] });
    expect(await parseStructured("yaml", "a: 1\n---\nb: 2\n")).toEqual([{ a: 1 }, { b: 2 }]);
  });

  it("parses TOML", async () => {
    expect(await parseStructured("toml", '[server]\nport = 8080\nname = "x"\n')).toEqual({ server: { port: 8080, name: "x" } });
  });

  it("renders a YAML file as a tree", async () => {
    render(<StructuredViewer format="yaml" source={"name: ci\nsteps:\n  - build\n  - test\n"} />);

    expect(await screen.findByText('"name"')).toBeInTheDocument();
    expect(screen.getByText('"build"')).toBeInTheDocument();
  });

  it("explains invalid files", async () => {
    render(<StructuredViewer format="toml" source={"this is = = not toml"} />);

    expect(await screen.findByRole("alert")).toHaveTextContent("This is not valid TOML");
  });
});

describe("LogViewer", () => {
  const log = [
    "2026-10-04T10:00:00Z INFO server started",
    "2026-10-04T10:00:01Z WARN slow request",
    "2026-10-04T10:00:02Z ERROR \u001b[31mdatabase down\u001b[0m",
    "continuation line with no level",
    "level=debug msg=cache"
  ].join("\n");

  it("finds a line's level", () => {
    expect(logLevel("2026 ERROR boom")).toBe("error");
    expect(logLevel("[warning] disk")).toBe("warn");
    expect(logLevel("level=debug msg=x")).toBe("debug");
    expect(logLevel("just text")).toBeNull();
    expect(logLevel("an error occurred in the information desk")).toBe("error");
  });

  it("strips colour codes and shows every line", () => {
    render(<LogViewer source={log} />);

    expect(screen.getByText(/ERROR database down/)).toBeInTheDocument();
    expect(screen.getByText("5 lines")).toBeInTheDocument();
  });

  it("filters by text", () => {
    render(<LogViewer source={log} />);

    fireEvent.change(screen.getByLabelText("Filter log lines"), { target: { value: "slow" } });

    return waitFor(() => {
      expect(screen.getByText("1 of 5 lines")).toBeInTheDocument();
      expect(screen.queryByText(/server started/)).not.toBeInTheDocument();
    });
  });

  it("hides a level", () => {
    render(<LogViewer source={log} />);

    fireEvent.click(screen.getByRole("button", { name: /^error 1$/ }));

    expect(screen.queryByText(/database down/)).not.toBeInTheDocument();
    expect(screen.getByText(/server started/)).toBeInTheDocument();
  });
});

describe("NotebookViewer", () => {
  const notebook = {
    metadata: { kernelspec: { language: "python" } },
    nbformat: 4,
    cells: [
      { cell_type: "markdown", source: ["# Title\n", "text"] },
      {
        cell_type: "code",
        execution_count: 3,
        source: "print('hi')",
        outputs: [
          { output_type: "stream", name: "stdout", text: ["hi\n"] },
          { output_type: "display_data", data: { "image/png": "iVBORw0KGgo=", "text/plain": "<Figure>" } },
          { output_type: "execute_result", data: { "text/html": '<b>bold</b><img src=x onerror="alert(1)"><script>alert(2)</script>' } },
          { output_type: "error", ename: "ValueError", evalue: "bad", traceback: ["\u001b[31mValueError\u001b[0m: bad"] }
        ]
      }
    ]
  };

  it("shows cells, their number and their saved outputs", async () => {
    render(<NotebookViewer source={JSON.stringify(notebook)} />);

    expect(await screen.findByRole("heading", { name: "Title" })).toBeInTheDocument();
    expect(screen.getByText("In [3]:")).toBeInTheDocument();
    expect(screen.getByText("hi", { selector: "pre" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Notebook output" })).toHaveAttribute("src", "data:image/png;base64,iVBORw0KGgo=");
    expect(screen.getByText("ValueError: bad")).toBeInTheDocument();
  });

  it("sanitizes HTML outputs", async () => {
    render(<NotebookViewer source={JSON.stringify(notebook)} />);

    expect(await screen.findByText("bold")).toBeInTheDocument();
    await waitFor(() => expect(document.querySelector("script")).toBeNull());
    for (const element of Array.from(document.querySelectorAll("img"))) {
      expect(element.getAttribute("onerror")).toBeNull();
    }
  });

  it("refuses what is not a notebook", () => {
    render(<NotebookViewer source={'{"hello":1}'} />);

    expect(screen.getByRole("alert")).toHaveTextContent("no cells found");
  });
});

describe("SourceCodeViewer previews", () => {
  it("previews JSON as a tree and switches to the source", () => {
    render(<SourceCodeViewer code={'{"a":1}'} path="/x/config.json" />);

    expect(screen.getByRole("tree")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "raw" }));
    expect(screen.queryByRole("tree")).not.toBeInTheDocument();
  });

  it("previews a log with its filter", () => {
    render(<SourceCodeViewer code={"INFO a\nERROR b\n"} path="/var/app.log" />);

    expect(within(document.body).getByLabelText("Filter log lines")).toBeInTheDocument();
  });
});
