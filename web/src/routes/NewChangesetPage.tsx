import { useMutation } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useMemo, useState, type FormEvent } from "react";

import type { SliceRef } from "../api/types";
import { useApi } from "../api/useApi";
import {
  FileEditForm,
  clientPreview,
  createEmptyEditDraft,
  prepareFileEdits,
  type FileEditDraft
} from "../components/changesets/FileEditForm";
import { GLOBAL_REF_NAME } from "../lib/globalRef";
import { useSelection } from "../state/selection";

export function NewChangesetPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { account } = useSelection();
  const [sliceInput, setSliceInput] = useState(() => (account ? `${account}/` : ""));
  const [baseCommitId, setBaseCommitId] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [rows, setRows] = useState<FileEditDraft[]>([createEmptyEditDraft()]);

  useEffect(() => {
    if (!sliceInput && account) {
      setSliceInput(`${account}/`);
    }
  }, [account, sliceInput]);

  const preview = useMemo(() => clientPreview(rows), [rows]);

  const validateMutation = useMutation({
    mutationFn: async () => {
      validateRows(rows);
      const authoringSlice = parseSliceRef(sliceInput);
      const resolvedSlice = await api.resolveSlice({ ref: authoringSlice });
      const resolvedRef = resolvedSlice.ref ?? authoringSlice;
      const baseCommit = baseCommitId.trim()
        ? baseCommitId.trim()
        : (await api.getRef({ refName: GLOBAL_REF_NAME })).commitId;

      if (!baseCommit) {
        throw new Error("Latest global state did not return a commit id.");
      }

      return {
        baseCommit,
        slice: formatSliceRef(resolvedRef)
      };
    }
  });

  const createMutation = useMutation({
    mutationFn: async () => {
      const authoringSlice = parseSliceRef(sliceInput);
      const resolvedSlice = await api.resolveSlice({ ref: authoringSlice });
      const resolvedRef = resolvedSlice.ref ?? authoringSlice;
      const baseCommit = baseCommitId.trim()
        ? baseCommitId.trim()
        : (await api.getRef({ refName: GLOBAL_REF_NAME })).commitId;

      if (!baseCommit) {
        throw new Error("Latest global state did not return a commit id.");
      }

      const fileEdits = await prepareFileEdits({
        rows,
        slice: resolvedRef,
        uploadBlob: api.uploadBlob
      });

      const changeset = await api.createChangeset({
        authoringSlice: resolvedRef,
        baseCommitId: baseCommit,
        title: title.trim(),
        description: description.trim()
      });

      if (!changeset.id) {
        throw new Error("CreateChangeset did not return a changeset id.");
      }

      await api.updateChangeset({
        changesetId: changeset.id,
        expectedCurrentPatchsetId: changeset.currentPatchsetId,
        baseCommitId: baseCommit,
        fileEdits
      });

      return changeset;
    },
    onSuccess: (changeset) => {
      const id = changeset.handle || changeset.id;
      if (!id) {
        return;
      }
      void navigate({
        to: "/changesets/$id",
        params: { id }
      });
    }
  });

  const submitCreate = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    createMutation.mutate();
  };

  const busy = createMutation.isPending || validateMutation.isPending;

  return (
    <section className="mx-auto w-full max-w-7xl">
      <div className="flex flex-col gap-4 border-b border-slate-200 pb-5 lg:flex-row lg:items-end lg:justify-between">
        <div>
          <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
            Changesets
          </p>
          <h1 className="mt-2 text-2xl font-semibold tracking-normal text-zinc-950">
            New Changeset
          </h1>
          <p className="mt-2 max-w-2xl text-sm text-slate-600">
            Create a draft changeset from explicit file edits. Pasted add and
            modify contents are uploaded as blobs before the initial patchset is
            added.
          </p>
        </div>
        <Link
          className="w-fit rounded-md border border-slate-300 bg-white px-4 py-2.5 text-sm font-medium text-zinc-800 transition hover:border-zinc-500 active:translate-y-px"
          to="/changesets"
        >
          Open Existing
        </Link>
      </div>

      <form className="mt-8 grid gap-6" onSubmit={submitCreate}>
        <section className="rounded-lg border border-slate-200 bg-white p-5">
          <div className="grid gap-4 lg:grid-cols-2">
            <label className="grid gap-2 text-sm font-medium text-zinc-800">
              Authoring slice
              <input
                className="h-10 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
                disabled={busy}
                onChange={(event) => setSliceInput(event.target.value)}
                placeholder="acme/payment"
                value={sliceInput}
              />
            </label>

            <label className="grid gap-2 text-sm font-medium text-zinc-800">
              Base commit
              <input
                className="h-10 rounded-md border border-slate-300 bg-white px-3 font-mono text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
                disabled={busy}
                onChange={(event) => setBaseCommitId(event.target.value)}
                placeholder="Resolved from latest global state when blank"
                value={baseCommitId}
              />
            </label>

            <label className="grid gap-2 text-sm font-medium text-zinc-800">
              Title
              <input
                className="h-10 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
                disabled={busy}
                onChange={(event) => setTitle(event.target.value)}
                placeholder="Fix payment app"
                value={title}
              />
            </label>
          </div>

          <label className="mt-4 grid gap-2 text-sm font-medium text-zinc-800">
            Description
            <textarea
              className="min-h-24 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm leading-6 text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
              disabled={busy}
              onChange={(event) => setDescription(event.target.value)}
              placeholder="Optional context for this draft"
              value={description}
            />
          </label>
        </section>

        <FileEditForm disabled={busy} onRowsChange={setRows} rows={rows} />

        {preview ? (
          <section className="rounded-lg border border-slate-200 bg-white">
            <div className="border-b border-slate-200 px-5 py-4">
              <h2 className="text-base font-semibold tracking-normal text-zinc-950">
                Client Preview
              </h2>
              <p className="mt-1 text-sm text-slate-600">
                This preview uses only pasted browser state. Persisted patchsets
                expose edit metadata, not full staged blob contents.
              </p>
            </div>
            <pre className="max-h-80 overflow-auto px-5 py-4 font-mono text-sm leading-6 text-slate-800">
              {preview}
            </pre>
          </section>
        ) : null}

        {validateMutation.isSuccess ? (
          <div className="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-900">
            Resolved {validateMutation.data.slice} at base commit{" "}
            <span className="font-mono">{validateMutation.data.baseCommit}</span>.
          </div>
        ) : null}

        {validateMutation.isError ? (
          <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800">
            {errorMessage(validateMutation.error)}
          </div>
        ) : null}

        {createMutation.isError ? (
          <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800">
            {errorMessage(createMutation.error)}
          </div>
        ) : null}

        <div className="flex flex-wrap gap-3">
          <button
            className="rounded-md border border-slate-300 bg-white px-4 py-2.5 text-sm font-medium text-zinc-800 transition hover:border-zinc-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
            disabled={busy}
            onClick={() => validateMutation.mutate()}
            type="button"
          >
            {validateMutation.isPending ? "Validating..." : "Validate"}
          </button>
          <button
            className="rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
            disabled={busy}
            type="submit"
          >
            {createMutation.isPending ? "Creating Draft..." : "Create Draft"}
          </button>
        </div>
      </form>
    </section>
  );
}

function parseSliceRef(value: string): SliceRef {
  const normalized = value.trim().replace(/^\/+|\/+$/g, "");
  const [account, slice, extra] = normalized.split("/");

  if (!account || !slice || extra) {
    throw new Error("Authoring slice must use account/slice format.");
  }

  return { account, slice };
}

function formatSliceRef(ref: SliceRef) {
  return `${ref.account ?? "unknown"}/${ref.slice ?? "unknown"}`;
}

function validateRows(rows: FileEditDraft[]) {
  const activeRows = rows.filter(
    (row) => row.path.trim() || (row.op !== "delete" && row.content.trim())
  );

  if (activeRows.length === 0) {
    throw new Error("Add at least one file edit.");
  }

  for (const row of activeRows) {
    if (!row.path.trim()) {
      throw new Error("Each file edit needs a path.");
    }
  }
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}
