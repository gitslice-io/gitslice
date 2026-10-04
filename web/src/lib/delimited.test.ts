import { describe, expect, it } from "vitest";

import { detectDelimiter, parseDelimited } from "./delimited";

describe("parseDelimited", () => {
  it("splits rows and fields", () => {
    expect(parseDelimited("a,b,c\n1,2,3\n", ",").rows).toEqual([
      ["a", "b", "c"],
      ["1", "2", "3"]
    ]);
  });

  it("handles quoted fields with delimiters, quotes and line breaks", () => {
    const text = 'name,note\n"Smith, Jo","said ""hi"""\n"two\nlines",x\n';
    expect(parseDelimited(text, ",").rows).toEqual([
      ["name", "note"],
      ["Smith, Jo", 'said "hi"'],
      ["two\nlines", "x"]
    ]);
  });

  it("accepts CRLF and CR line ends, a BOM, and no final newline", () => {
    expect(parseDelimited("﻿a,b\r\n1,2\r3,4", ",").rows).toEqual([
      ["a", "b"],
      ["1", "2"],
      ["3", "4"]
    ]);
  });

  it("keeps empty fields and skips blank lines", () => {
    expect(parseDelimited("a,,c\n\n,,\nx,\n", ",").rows).toEqual([
      ["a", "", "c"],
      ["", "", ""],
      ["x", ""]
    ]);
  });

  it("treats a quote inside an unquoted field as text", () => {
    expect(parseDelimited('5" pipe,ok\n', ",").rows).toEqual([['5" pipe', "ok"]]);
  });

  it("opens a quote right after a delimiter", () => {
    expect(parseDelimited('a,"b,c",d\n', ",").rows).toEqual([["a", "b,c", "d"]]);
  });

  it("stops at maxRows and says so", () => {
    const parsed = parseDelimited("1\n2\n3\n4\n", ",", 2);
    expect(parsed.rows).toEqual([["1"], ["2"]]);
    expect(parsed.truncated).toBe(true);
    expect(parseDelimited("1\n2\n", ",", 2).truncated).toBe(false);
  });

  it("returns no rows for an empty file", () => {
    expect(parseDelimited("", ",").rows).toEqual([]);
  });
});

describe("detectDelimiter", () => {
  it("uses tabs for .tsv", () => {
    expect(detectDelimiter("/data/x.tsv", "a,b\n")).toBe("\t");
  });

  it("finds the separator that gives a consistent column count", () => {
    expect(detectDelimiter("/x.csv", "a;b;c\n1;2;3\n4;5;6\n")).toBe(";");
    expect(detectDelimiter("/x.csv", "a\tb\n1\t2\n")).toBe("\t");
    expect(detectDelimiter("/x.csv", "a|b|c\n1|2|3\n")).toBe("|");
    expect(detectDelimiter("/x.csv", "a,b,c\n1,2,3\n")).toBe(",");
  });

  it("is not fooled by delimiters inside quotes", () => {
    expect(detectDelimiter("/x.csv", '"a;b",c\n"d;e",f\n')).toBe(",");
  });

  it("defaults to a comma", () => {
    expect(detectDelimiter("/x.csv", "just one column\nand another\n")).toBe(",");
  });
});
