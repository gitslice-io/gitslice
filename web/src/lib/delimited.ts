// Parsing for delimiter-separated text (CSV, TSV), for the file viewer's table
// preview. The parser follows RFC 4180 where it matters for real files: quoted
// fields may hold delimiters, line breaks and doubled quotes, and rows end at
// \n, \r\n or \r.

export type Delimiter = "," | "\t" | ";" | "|";

const CANDIDATES: Delimiter[] = [",", "\t", ";", "|"];

export interface ParsedTable {
  rows: string[][];
  // True when parsing stopped at maxRows and the text holds more.
  truncated: boolean;
}

export function parseDelimited(
  text: string,
  delimiter: Delimiter,
  maxRows = Number.POSITIVE_INFINITY
): ParsedTable {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let inQuotes = false;
  let fieldStarted = false;
  let truncated = false;

  let i = text.charCodeAt(0) === 0xfeff ? 1 : 0; // byte order mark

  const endField = () => {
    row.push(field);
    field = "";
    fieldStarted = false;
  };
  // Returns false once maxRows is reached.
  const endRow = () => {
    endField();
    // A blank line is not a row.
    if (!(row.length === 1 && row[0] === "")) {
      rows.push(row);
    }
    row = [];
    return rows.length < maxRows;
  };

  for (; i < text.length; i++) {
    const char = text[i];
    if (inQuotes) {
      if (char === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i++;
        } else {
          inQuotes = false;
        }
      } else {
        field += char;
      }
      continue;
    }
    if (char === '"' && !fieldStarted) {
      inQuotes = true;
      fieldStarted = true;
    } else if (char === delimiter) {
      endField();
      continue;
    } else if (char === "\n" || char === "\r") {
      if (char === "\r" && text[i + 1] === "\n") {
        i++;
      }
      if (!endRow()) {
        truncated = i + 1 < text.length;
        return { rows, truncated };
      }
    } else {
      field += char;
      fieldStarted = true;
    }
  }
  if (field !== "" || row.length > 0 || inQuotes) {
    endRow();
  }
  return { rows, truncated };
}

// detectDelimiter picks the separator for a file. A .tsv is tab separated; for
// anything else the candidate that splits the first rows into the same number
// of columns, more than one, wins. Comma is the default.
export function detectDelimiter(path: string, text: string): Delimiter {
  if (path.toLowerCase().endsWith(".tsv")) {
    return "\t";
  }
  const sample = text.slice(0, 32 * 1024);
  let best: Delimiter = ",";
  let bestScore = 0;
  for (const candidate of CANDIDATES) {
    const { rows } = parseDelimited(sample, candidate, 20);
    if (rows.length === 0) {
      continue;
    }
    const counts = new Map<number, number>();
    for (const row of rows) {
      counts.set(row.length, (counts.get(row.length) ?? 0) + 1);
    }
    let columns = 0;
    let agreeing = 0;
    for (const [count, rowsWithCount] of counts) {
      if (rowsWithCount > agreeing || (rowsWithCount === agreeing && count > columns)) {
        columns = count;
        agreeing = rowsWithCount;
      }
    }
    if (columns < 2) {
      continue;
    }
    const score = (agreeing / rows.length) * columns;
    if (score > bestScore) {
      best = candidate;
      bestScore = score;
    }
  }
  return best;
}
