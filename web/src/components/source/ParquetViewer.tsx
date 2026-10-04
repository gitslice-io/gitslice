import { useEffect, useState } from "react";

import { useBytes } from "../../lib/bytes";
import { DataTable, cellText } from "./DataTable";

// An Apache Parquet file as a table, read in the browser. The reader loads on
// first use. Only the first rows are read; the schema and row count come from
// the file's footer.

const MAX_ROWS = 50_000;

interface Loaded {
  columns: string[];
  rows: string[][];
  totalRows: number;
}

type State = { status: "loading" } | { status: "error"; message: string } | { status: "ready"; loaded: Loaded };

export function ParquetViewer({ data }: { data: string }) {
  const bytes = useBytes(data);
  const [state, setState] = useState<State>({ status: "loading" });

  useEffect(() => {
    let current = true;
    if (!bytes) {
      setState({ status: "error", message: "the file could not be decoded" });
      return;
    }
    setState({ status: "loading" });
    (async (): Promise<Loaded> => {
      const { parquetMetadata, parquetReadObjects } = await import("hyparquet");
      const buffer = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer;
      const metadata = parquetMetadata(buffer);
      const columns = metadata.schema.slice(1).filter((element) => element.num_children === undefined).map((element) => element.name);
      const objects = await parquetReadObjects({ file: buffer, metadata, rowEnd: MAX_ROWS });
      return {
        columns,
        rows: objects.map((object) => columns.map((column) => cellText((object as Record<string, unknown>)[column]))),
        totalRows: Number(metadata.num_rows)
      };
    })().then(
      (loaded) => current && setState({ status: "ready", loaded }),
      (error) => current && setState({ status: "error", message: error instanceof Error ? error.message : "unreadable file" })
    );
    return () => {
      current = false;
    };
  }, [bytes]);

  if (state.status === "loading") {
    return <p className="p-4 text-sm text-slate-500 dark:text-zinc-400" role="status">Reading the Parquet file…</p>;
  }
  if (state.status === "error") {
    return (
      <p className="bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200" role="alert">
        The Parquet file could not be read: {state.message}.
      </p>
    );
  }
  const { columns, rows, totalRows } = state.loaded;
  return (
    <DataTable
      header={columns}
      limitNote={`The file has ${totalRows.toLocaleString()} rows; only the first ${MAX_ROWS.toLocaleString()} are previewed.`}
      rows={rows}
      summary={`${totalRows.toLocaleString()} rows in the file`}
      truncated={totalRows > rows.length}
    />
  );
}
