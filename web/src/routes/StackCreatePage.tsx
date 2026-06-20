import { useMutation, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent } from "react";

import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { normalizeRepositoryPath } from "../components/source/sourceUtils";
import {
  SliceNotice,
  SlicePageHeader
} from "../components/slices/SlicePageParts";
import { GLOBAL_REF_NAME } from "../lib/globalRef";
import { useSelection } from "../state/selection";
import {
  getErrorMessage,
  parseSliceSearch,
  primaryButtonClass,
  secondaryButtonClass,
  sliceRefLabel
} from "./stackPageUtils";

interface StackCreateSearch {
  slice?: unknown;
}

export function StackCreatePage() {
  const api = useApi();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as StackCreateSearch;
  const initialSlice = sliceRefLabel(parseSliceSearch(search.slice));
  const { account } = useSelection();
  const [slice, setSlice] = useState(initialSlice);
  const [targetRef, setTargetRef] = useState(GLOBAL_REF_NAME);
  const [baseCommitId, setBaseCommitId] = useState("");
  const [stackTitle, setStackTitle] = useState("");
  const [entryTitle, setEntryTitle] = useState("");
  const [entryDescription, setEntryDescription] = useState("");
  const [filePath, setFilePath] = useState("");
  const [fileContent, setFileContent] = useState("");
  const [formError, setFormError] = useState("");

  const refQuery = useQuery({
    queryKey: ["globalRef", GLOBAL_REF_NAME],
    queryFn: () => api.getRef({ refName: GLOBAL_REF_NAME })
  });

  const resolvedBaseCommit = baseCommitId.trim() || refQuery.data?.commitId || "";
  const parsedSlice = useMemo(() => parseSliceSearch(slice), [slice]);

  const createMutation = useMutation({
    mutationFn: async () => {
      const authoringSlice = parseSliceSearch(slice);
      if (!authoringSlice) {
        throw new Error("Enter a slice as account:slice.");
      }
      const title = stackTitle.trim() || entryTitle.trim();
      if (!title) {
        throw new Error("Enter a dependency title or first changeset title.");
      }
      if ((filePath.trim() || fileContent) && !entryTitle.trim()) {
        throw new Error("Enter a first changeset title before adding a file edit.");
      }

      const stack = await api.createStack({
        authoringSlice,
        baseCommitId: baseCommitId.trim(),
        targetRef: targetRef.trim(),
        title
      });

      const firstEntryTitle = entryTitle.trim();
      if (!firstEntryTitle) {
        return { stack, firstEntryId: "" };
      }

      const firstEntry = await api.addStackEntry({
        description: entryDescription.trim(),
        stackId: stack.id,
        title: firstEntryTitle
      });

      const trimmedPath = filePath.trim();
      if (trimmedPath || fileContent) {
        if (!trimmedPath) {
          throw new Error("Enter a file path for the first patchset.");
        }
        if (!firstEntry.id) {
          throw new Error("The API did not return a changeset id.");
        }

        const uploaded = await api.uploadBlob({
          data: utf8ToBase64(fileContent),
          slice: authoringSlice
        });
        if (!uploaded.blobId || !uploaded.contentHash) {
          throw new Error("UploadBlob did not return blob metadata.");
        }

        await api.updateChangeset({
          baseCommitId: stack.baseCommitId || resolvedBaseCommit,
          baseKind: "commit",
          changesetId: firstEntry.id,
          fileEdits: [
            {
              blobId: uploaded.blobId,
              contentHash: uploaded.contentHash,
              mode: 0o100644,
              op: "upsert",
              path: normalizeRepositoryPath(trimmedPath)
            }
          ]
        });
      }

      return { stack, firstEntryId: firstEntry.id || "" };
    },
    onError: (error) => setFormError(getErrorMessage(error)),
    onMutate: () => setFormError(""),
    onSuccess: ({ firstEntryId, stack }) => {
      if (!stack.id) {
        setFormError("The API did not return a dependency id.");
        return;
      }
      void navigate({
        params: { id: stack.id },
        search: firstEntryId ? ({ entry: firstEntryId } as never) : ({} as never),
        to: "/dependencies/$id"
      });
    }
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    createMutation.mutate();
  };

  return (
    <section className="mx-auto w-full max-w-[72rem]">
      <div className="mb-4">
        <Breadcrumb
          items={[
            { label: "Dependencies", to: "/dependencies" },
            { label: "Create" }
          ]}
        />
      </div>
      <SlicePageHeader
        description="Create a root changeset and optionally add the first patchset file."
        eyebrow="Dependencies"
        title="Create changeset"
      />

      <form
        className="mt-8 grid gap-6 rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
        onSubmit={submit}
      >
        <div className="grid gap-4 lg:grid-cols-2">
          <label className="grid gap-2 text-sm font-medium text-zinc-800">
            Authoring slice
            <input
              className={inputClass}
              onChange={(event) => setSlice(event.target.value)}
              placeholder={account ? `${account}:main` : "acme:payment"}
              value={slice}
            />
          </label>
          <label className="grid gap-2 text-sm font-medium text-zinc-800">
            Target ref
            <input
              className={inputClass}
              onChange={(event) => setTargetRef(event.target.value)}
              value={targetRef}
            />
          </label>
        </div>

        <label className="grid gap-2 text-sm font-medium text-zinc-800">
          Base commit
          <input
            className={inputClass}
            onChange={(event) => setBaseCommitId(event.target.value)}
            placeholder={refQuery.data?.commitId || "Current refs/global/main"}
            value={baseCommitId}
          />
          <span className="break-all text-xs font-normal text-slate-500">
            {resolvedBaseCommit
              ? `Using ${resolvedBaseCommit}`
              : "The server will use the current target ref commit."}
          </span>
        </label>

        <label className="grid gap-2 text-sm font-medium text-zinc-800">
          Dependency title
          <input
            className={inputClass}
            onChange={(event) => setStackTitle(event.target.value)}
            placeholder="Payment parser rollout"
            value={stackTitle}
          />
        </label>

        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)]">
          <label className="grid gap-2 text-sm font-medium text-zinc-800">
            First changeset title
            <input
              className={inputClass}
              onChange={(event) => setEntryTitle(event.target.value)}
              placeholder="Introduce payment parser"
              value={entryTitle}
            />
          </label>
          <label className="grid gap-2 text-sm font-medium text-zinc-800">
            First changeset description
            <textarea
              className="min-h-28 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
              onChange={(event) => setEntryDescription(event.target.value)}
              placeholder="Optional review context"
              value={entryDescription}
            />
          </label>
        </div>

        <div className="rounded-lg border border-slate-200 bg-slate-50 p-4">
          <h2 className="text-sm font-semibold text-zinc-950">
            First patchset file
          </h2>
          <div className="mt-4 grid gap-4 lg:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
            <label className="grid gap-2 text-sm font-medium text-zinc-800">
              Path
              <input
                className={inputClass}
                onChange={(event) => setFilePath(event.target.value)}
                placeholder="/acme/payment/parser.go"
                value={filePath}
              />
            </label>
            <label className="grid gap-2 text-sm font-medium text-zinc-800">
              Content
              <textarea
                className="min-h-36 rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
                onChange={(event) => setFileContent(event.target.value)}
                placeholder="package payment"
                value={fileContent}
              />
            </label>
          </div>
        </div>

        {!parsedSlice && slice.trim() ? (
          <SliceNotice title="Invalid slice format" tone="error">
            Use the account:slice form, for example acme:payment.
          </SliceNotice>
        ) : null}

        {formError ? (
          <SliceNotice title="Could not create changeset" tone="error">
            {formError}
          </SliceNotice>
        ) : null}

        <div className="flex flex-wrap justify-end gap-2">
          <button
            className={secondaryButtonClass}
            onClick={() => {
              void navigate({ to: "/dependencies" });
            }}
            type="button"
          >
            Cancel
          </button>
          <button
            className={primaryButtonClass}
            disabled={createMutation.isPending}
            type="submit"
          >
            {createMutation.isPending ? "Creating..." : "Create changeset"}
          </button>
        </div>
      </form>
    </section>
  );
}

const inputClass =
  "h-11 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200";

function utf8ToBase64(value: string) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (let index = 0; index < bytes.length; index += 1) {
    binary += String.fromCharCode(bytes[index]);
  }
  return window.btoa(binary);
}
