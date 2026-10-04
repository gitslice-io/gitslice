import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { SourceCodeViewer } from "./SourceCodeViewer";
import { TableViewer, looksLikeHeader, numericColumns } from "./TableViewer";

afterEach(cleanup);

describe("TableViewer", () => {
  it("renders a header row and body rows", () => {
    render(<TableViewer path="/data/people.csv" source={"name,age\nAda,36\nGrace,45\n"} />);

    expect(screen.getByRole("columnheader", { name: "name" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "age" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "Ada" })).toBeInTheDocument();
    expect(screen.getByText(/2 rows · 2 columns · separated by commas/)).toBeInTheDocument();
  });

  it("right-aligns numeric columns", () => {
    render(<TableViewer path="/data/people.csv" source={"name,age\nAda,36\nGrace,45\n"} />);

    expect(screen.getByRole("cell", { name: "36" })).toHaveClass("text-right");
    expect(screen.getByRole("cell", { name: "Ada" })).toHaveClass("text-left");
  });

  it("can treat the first row as data", () => {
    render(<TableViewer path="/data/p.csv" source={"name,age\nAda,36\n"} />);

    fireEvent.click(screen.getByLabelText("First row is a header"));

    expect(screen.queryByRole("columnheader", { name: "name" })).not.toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "name" })).toBeInTheDocument();
  });

  it("shows more rows on request", () => {
    const rows = Array.from({ length: 450 }, (_, i) => `r${i},${i}`).join("\n");
    render(<TableViewer path="/data/big.csv" source={`key,value\n${rows}\n`} />);

    expect(screen.getByText("Showing 200 of 450 rows")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "r449" })).toBeInTheDocument();
  });

  it("pads short rows and detects other separators", () => {
    render(<TableViewer path="/data/x.csv" source={"a;b;c\n1;2\n4;5;6\n"} />);

    expect(screen.getByText(/separated by semicolons/)).toBeInTheDocument();
    const rows = screen.getAllByRole("row");
    expect(within(rows[2]).getAllByRole("cell")).toHaveLength(3);
  });

  it("does not interpret cell contents as markup", () => {
    render(<TableViewer path="/data/x.csv" source={'a,b\n"<img src=x onerror=alert(1)>",2\n'} />);

    expect(screen.getByRole("cell", { name: "<img src=x onerror=alert(1)>" })).toBeInTheDocument();
    expect(document.querySelector("img")).toBeNull();
  });

  it("says when the file is empty", () => {
    render(<TableViewer path="/data/empty.csv" source="" />);

    expect(screen.getByText("This file is empty.")).toBeInTheDocument();
  });
});

describe("table heuristics", () => {
  it("takes a first row of text above data as a header", () => {
    expect(looksLikeHeader([["id", "name"], ["1", "a"]])).toBe(true);
    expect(looksLikeHeader([["1", "2"], ["3", "4"]])).toBe(false);
    expect(looksLikeHeader([["only"]])).toBe(false);
  });

  it("marks columns of numbers", () => {
    expect(numericColumns([["a", "1", ""], ["b", "2.5", ""], ["c", "-3e2", ""]], 3)).toEqual([false, true, false]);
  });
});

describe("SourceCodeViewer with a table preview", () => {
  it("previews .csv as a table and switches to the raw text", () => {
    render(<SourceCodeViewer code={"a,b\n1,2\n"} path="/data/x.csv" />);

    expect(screen.getByRole("table")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "raw" }));
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("does not offer a preview for other files", () => {
    render(<SourceCodeViewer code={"package main\n"} path="/src/main.go" />);

    expect(screen.queryByRole("button", { name: "preview" })).not.toBeInTheDocument();
  });
});
