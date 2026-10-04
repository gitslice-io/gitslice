import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { zipSync, strToU8 } from "fflate";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BinaryViewer } from "./BinaryViewer";

const created: string[] = [];
const revoked: string[] = [];

beforeEach(() => {
  created.length = 0;
  revoked.length = 0;
  URL.createObjectURL = vi.fn(() => {
    const url = `blob:test/${created.length}`;
    created.push(url);
    return url;
  });
  URL.revokeObjectURL = vi.fn((url: string) => {
    revoked.push(url);
  });
});
afterEach(cleanup);

function toBase64(bytes: Uint8Array) {
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return window.btoa(binary);
}

describe("media files", () => {
  it("shows a PDF in a frame with links to open and save it", () => {
    render(<BinaryViewer data={toBase64(strToU8("%PDF-1.4"))} kind="pdf" path="/docs/spec.pdf" />);

    expect(screen.getByTitle("Preview of spec.pdf")).toHaveAttribute("src", "blob:test/0");
    expect(screen.getByRole("link", { name: "Download" })).toHaveAttribute("download", "spec.pdf");
  });

  it("plays audio and video with the browser's controls", () => {
    const { unmount } = render(<BinaryViewer data={toBase64(strToU8("x"))} kind="audio" path="/a/song.mp3" />);
    expect(screen.getByLabelText("Audio song.mp3")).toHaveAttribute("controls");
    unmount();

    render(<BinaryViewer data={toBase64(strToU8("x"))} kind="video" path="/a/clip.webm" />);
    expect(screen.getByLabelText("Video clip.webm")).toHaveAttribute("controls");
  });

  it("releases the blob URL when the view goes away", () => {
    const { unmount } = render(<BinaryViewer data={toBase64(strToU8("x"))} kind="pdf" path="/a/b.pdf" />);
    unmount();

    expect(revoked).toEqual(created);
  });

  it("says when the file is empty", () => {
    render(<BinaryViewer data="" kind="pdf" path="/a/empty.pdf" />);

    expect(screen.getByText("This file is empty.")).toBeInTheDocument();
  });
});

function workbook(sheets: Record<string, string[][]>) {
  const names = Object.keys(sheets);
  const files: Record<string, Uint8Array> = {
    "[Content_Types].xml": strToU8(
      '<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>' +
        names.map((_, i) => `<Override PartName="/xl/worksheets/sheet${i + 1}.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`).join("") +
        "</Types>"
    ),
    "_rels/.rels": strToU8(
      '<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>'
    ),
    "xl/workbook.xml": strToU8(
      '<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>' +
        names.map((name, i) => `<sheet name="${name}" sheetId="${i + 1}" r:id="rId${i + 1}"/>`).join("") +
        "</sheets></workbook>"
    ),
    "xl/_rels/workbook.xml.rels": strToU8(
      '<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">' +
        names.map((_, i) => `<Relationship Id="rId${i + 1}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet${i + 1}.xml"/>`).join("") +
        "</Relationships>"
    )
  };
  names.forEach((name, i) => {
    const rows = sheets[name]
      .map(
        (row, r) =>
          `<row r="${r + 1}">` +
          row.map((cell, c) => `<c r="${String.fromCharCode(65 + c)}${r + 1}" t="inlineStr"><is><t>${cell}</t></is></c>`).join("") +
          "</row>"
      )
      .join("");
    files[`xl/worksheets/sheet${i + 1}.xml`] = strToU8(
      `<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>${rows}</sheetData></worksheet>`
    );
  });
  return zipSync(files);
}

describe("spreadsheets", () => {
  it("shows each sheet of a workbook as a tab", async () => {
    const data = toBase64(workbook({ People: [["name", "city"], ["Ada", "London"]], Other: [["k"], ["v"]] }));
    render(<BinaryViewer data={data} kind="spreadsheet" path="/data/book.xlsx" />);

    expect(await screen.findByRole("cell", { name: "Ada" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "city" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Other" }));
    expect(await screen.findByRole("cell", { name: "v" })).toBeInTheDocument();
  });

  it("explains a file that is not a workbook", async () => {
    render(<BinaryViewer data={toBase64(strToU8("not a zip"))} kind="spreadsheet" path="/data/bad.xlsx" />);

    expect(await screen.findByRole("alert")).toHaveTextContent("could not be read");
  });
});

// name (string), age (int32), id (int64) for Alice, Bob and Charlie, written by
// hyparquet-writer: its string encoding does not run under jsdom.
const PEOPLE_PARQUET =
  "UEFSMRUGFToVPlwVBhUAFQYVABUEFQAAAAMHG2gFAAAAQWxpY2UDAAAAQm9iBwAAAENoYXJsaWUVBhUcFSBcFQYVABUGFQAVBBUAAAADBwwsGQAAAB4AAAAjAAAAFQYVNBUsXBUGFQAVBhUAFQQVAAAAAwcYBAEACQEAAgkHIAADAAAAAAAAABUEGUxIBHJvb3QVBgAVDCUCGARuYW1lJQAAFQIlAhgDYWdlABUEJQIYAmlkABYGGRwZPCYIHBUMGRUAGRgEbmFtZRUCFgYWaBZoJgg8NgAoB0NoYXJsaWUYBUFsaWNlABkcFQYVABUCAAAAJnAcFQIZFQAZGANhZ2UVAhYGFkoWSiZwPDYAKAQjAAAAGAQZAAAAABkcFQYVABUCAAAAJroBHBUEGRUAGRgCaWQVAhYGFlYWVia6ATw2ACgIAwAAAAAAAAAYCAEAAAAAAAAAABkcFQYVABUCAAAAFogCFgYAKAloeXBhcnF1ZXQA7AAAAFBBUjE=";

describe("Parquet files", () => {
  it("shows the columns and rows", async () => {
    render(<BinaryViewer data={PEOPLE_PARQUET} kind="parquet" path="/data/people.parquet" />);

    expect(await screen.findByRole("cell", { name: "Charlie" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "age" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "35" })).toHaveClass("text-right");
    expect(screen.getByText(/3 rows in the file/)).toBeInTheDocument();
  });

  it("explains a file that is not Parquet", async () => {
    render(<BinaryViewer data={toBase64(strToU8("hello"))} kind="parquet" path="/data/bad.parquet" />);

    expect(await screen.findByRole("alert")).toHaveTextContent("could not be read");
  });
});
