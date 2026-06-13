import type { FileEdit, SliceRef, UploadBlobResponse } from "../../api/types";
import { cn } from "../../lib/cn";

export type FileEditOp = "add" | "modify" | "delete";

export interface FileEditDraft {
  id: string;
  op: FileEditOp;
  path: string;
  content: string;
}

interface FileEditFormProps {
  disabled?: boolean;
  rows: FileEditDraft[];
  title?: string;
  onRowsChange(rows: FileEditDraft[]): void;
}

interface PrepareFileEditsOptions {
  rows: FileEditDraft[];
  slice: SliceRef;
  uploadBlob(request: {
    contentHash?: string;
    data?: string;
    slice?: SliceRef;
  }): Promise<UploadBlobResponse>;
}

let nextEditId = 1;

export function createEmptyEditDraft(op: FileEditOp = "modify"): FileEditDraft {
  const id =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? crypto.randomUUID()
      : `edit-${nextEditId++}`;

  return {
    id,
    op,
    path: "",
    content: ""
  };
}

export function FileEditForm({
  disabled = false,
  rows,
  title = "File edits",
  onRowsChange
}: FileEditFormProps) {
  const updateRow = (id: string, patch: Partial<FileEditDraft>) => {
    onRowsChange(rows.map((row) => (row.id === id ? { ...row, ...patch } : row)));
  };

  const removeRow = (id: string) => {
    if (rows.length === 1) {
      onRowsChange([createEmptyEditDraft()]);
      return;
    }
    onRowsChange(rows.filter((row) => row.id !== id));
  };

  const addRow = () => {
    onRowsChange([...rows, createEmptyEditDraft()]);
  };

  return (
    <section className="rounded-lg border border-slate-200 bg-white">
      <div className="border-b border-slate-200 px-5 py-4">
        <h2 className="text-base font-semibold tracking-normal text-zinc-950">
          {title}
        </h2>
        <p className="mt-1 text-sm text-slate-600">
          Add account-rooted paths and paste the complete file contents for add
          or modify rows.
        </p>
      </div>

      <div className="divide-y divide-slate-200">
        {rows.map((row, index) => (
          <div className="grid gap-4 px-5 py-5 lg:grid-cols-[minmax(0,1fr)_9rem]" key={row.id}>
            <div className="grid gap-4">
              <div className="grid gap-4 sm:grid-cols-[9rem_minmax(0,1fr)]">
                <label className="grid gap-2 text-sm font-medium text-zinc-800">
                  Operation
                  <select
                    className="h-10 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
                    disabled={disabled}
                    onChange={(event) =>
                      updateRow(row.id, { op: event.target.value as FileEditOp })
                    }
                    value={row.op}
                  >
                    <option value="modify">modify</option>
                    <option value="add">add</option>
                    <option value="delete">delete</option>
                  </select>
                </label>

                <label className="grid gap-2 text-sm font-medium text-zinc-800">
                  Path
                  <input
                    className="h-10 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
                    disabled={disabled}
                    onChange={(event) => updateRow(row.id, { path: event.target.value })}
                    placeholder="/acme/payment/app.go"
                    value={row.path}
                  />
                </label>
              </div>

              <label
                className={cn(
                  "grid gap-2 text-sm font-medium text-zinc-800",
                  row.op === "delete" && "text-slate-500"
                )}
              >
                Pasted content
                <textarea
                  className="min-h-40 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm leading-6 text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
                  disabled={disabled || row.op === "delete"}
                  onChange={(event) => updateRow(row.id, { content: event.target.value })}
                  placeholder={
                    row.op === "delete"
                      ? "No content is uploaded for delete rows."
                      : "Paste the full file contents here."
                  }
                  value={row.op === "delete" ? "" : row.content}
                />
              </label>
            </div>

            <div className="flex items-start justify-between gap-3 lg:flex-col lg:justify-start">
              <div className="rounded-md bg-slate-100 px-2.5 py-1 text-xs font-medium text-slate-600">
                Row {index + 1}
              </div>
              <button
                className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 transition hover:border-slate-400 hover:text-zinc-950 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
                disabled={disabled}
                onClick={() => removeRow(row.id)}
                type="button"
              >
                Remove
              </button>
            </div>
          </div>
        ))}
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-slate-200 px-5 py-4">
        <p className="text-sm text-slate-600">
          Delete rows only send the path. Add and modify rows upload the pasted
          bytes before the patchset is created.
        </p>
        <button
          className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-zinc-800 transition hover:border-zinc-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
          disabled={disabled}
          onClick={addRow}
          type="button"
        >
          Add Row
        </button>
      </div>
    </section>
  );
}

export async function prepareFileEdits({
  rows,
  slice,
  uploadBlob
}: PrepareFileEditsOptions): Promise<FileEdit[]> {
  if (!slice.account || !slice.slice) {
    throw new Error("Authoring slice must include both account and slice.");
  }

  const activeRows = rows.filter(
    (row) => row.path.trim() || (row.op !== "delete" && row.content.length > 0)
  );

  if (activeRows.length === 0) {
    throw new Error("Add at least one file edit.");
  }

  const edits: FileEdit[] = [];
  for (const row of activeRows) {
    const path = canonicalClientPath(row.path);

    if (row.op === "delete") {
      edits.push({ op: "delete", path });
      continue;
    }

    const uploaded = await uploadBlob({
      data: utf8ToBase64(row.content),
      slice
    });

    if (!uploaded.blobId) {
      throw new Error(`Upload did not return a blob id for ${path}.`);
    }

    edits.push({
      op: row.op === "add" ? "add" : "update",
      path,
      blobId: uploaded.blobId,
      contentHash: uploaded.contentHash
    });
  }

  return edits;
}

export function clientPreview(rows: FileEditDraft[]) {
  return rows
    .filter((row) => row.path.trim() || row.content.trim())
    .map((row) => {
      const path = row.path.trim() || "(path not set)";
      if (row.op === "delete") {
        return `delete ${path}`;
      }
      const lines = row.content.split("\n").slice(0, 8);
      return `${row.op} ${path}\n${lines.map((line) => `  ${line}`).join("\n")}`;
    })
    .join("\n\n");
}

function canonicalClientPath(path: string) {
  const trimmed = path.trim().split("\\").join("/");
  if (!trimmed) {
    throw new Error("Each file edit needs a path.");
  }
  return trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
}

function utf8ToBase64(value: string) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  const chunkSize = 0x8000;

  for (let index = 0; index < bytes.length; index += chunkSize) {
    const chunk = bytes.subarray(index, index + chunkSize);
    binary += String.fromCharCode(...chunk);
  }

  return btoa(binary);
}
