import type { ChangesetStack, ChangesetStackEntry } from "../../api/types";
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useApi } from "../../api/useApi";
import { normalizeRepositoryPath } from "../../components/source/sourceUtils";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { getErrorMessage } from "../stackPageUtils";
import { currentPatchset } from "../stackPageUtils";
import { base64ToUtf8 } from "./stackUtils";
import { inputClass } from "./stackUtils";
import { PreviewDirectory } from "./PreviewDirectory";

export function EntryPreviewTree({
  entry,
  stack
}: {
  entry: ChangesetStackEntry;
  stack: ChangesetStack;
}) {
  const api = useApi();
  const patchset = currentPatchset(entry.changeset);
  const rootTreeId = patchset?.resultTreeId || "";
  const [path, setPath] = useState("/");
  const normalizedPath = normalizeRepositoryPath(path || "/");

  useEffect(() => {
    setPath("/");
  }, [entry.changesetId, rootTreeId]);

  const previewQuery = useQuery({
    enabled: Boolean(rootTreeId),
    queryKey: ["stackEntryPreviewTree", rootTreeId, normalizedPath],
    queryFn: async () => {
      const resolved = await api.resolvePath({
        path: normalizedPath,
        rootTreeId
      });
      const resolvedEntry = resolved.entry;
      if (resolvedEntry?.kind === "ENTRY_KIND_FILE") {
        const read = await api.readFile({
          path: resolvedEntry.path || normalizedPath,
          rootTreeId
        });
        return {
          entry: resolvedEntry,
          fileText: base64ToUtf8(read.data || ""),
          kind: "file" as const
        };
      }

      const listed = await api.listDirectory({
        pageSize: 100,
        path: resolvedEntry?.path || normalizedPath,
        rootTreeId,
        slice: stack.authoringSlice
      });
      return {
        entries: listed.entries ?? [],
        entry: resolvedEntry,
        kind: "directory" as const
      };
    }
  });

  if (!rootTreeId) {
    return (
      <SliceNotice title="No preview tree">
        Add a patchset to this entry before browsing its materialized tree.
      </SliceNotice>
    );
  }

  const preview = previewQuery.data;

  return (
    <div className="rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="border-b border-slate-200 px-4 py-3">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <h2 className="text-sm font-semibold text-zinc-950">Preview tree</h2>
            <p className="mt-1 text-xs text-slate-500">
              Materialized from patchset {patchset?.number || "current"}.
            </p>
          </div>
          <form
            className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row lg:max-w-xl"
            onSubmit={(event) => {
              event.preventDefault();
              setPath(normalizedPath);
            }}
          >
            <input
              className={inputClass}
              onChange={(event) => setPath(event.target.value)}
              placeholder="/acme/payment"
              value={path}
            />
            <button className="inline-flex items-center justify-center rounded-md border border-slate-300 bg-white px-4 py-2.5 text-sm font-medium text-slate-700 transition hover:bg-slate-50 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60" type="submit">
              Browse
            </button>
          </form>
        </div>
      </div>

      {previewQuery.isPending ? (
        <div className="px-4 py-6 text-sm text-slate-600">Loading preview...</div>
      ) : null}
      {previewQuery.isError ? (
        <div className="px-4 py-4">
          <SliceNotice title="Preview unavailable" tone="error">
            {getErrorMessage(previewQuery.error)}
          </SliceNotice>
        </div>
      ) : null}
      {preview?.kind === "directory" ? (
        <PreviewDirectory
          entries={preview.entries}
          onOpen={(nextPath) => setPath(nextPath)}
          path={preview.entry?.path || normalizedPath}
        />
      ) : null}
      {preview?.kind === "file" ? (
        <pre className="max-h-96 overflow-auto border-t border-slate-200 bg-slate-950 px-4 py-4 text-sm leading-6 text-slate-100">
          <code>{preview.fileText}</code>
        </pre>
      ) : null}
    </div>
  );
}