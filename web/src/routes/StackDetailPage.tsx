import {
  type UseQueryResult,
  useMutation,
  useQuery,
  useQueryClient
} from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";

import type {
  ChangesetStack,
  ChangesetStackEntry,
  DiffChangesetResponse,
  TreeEntry
} from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { DiffViewer } from "../components/diff/DiffViewer";
import { normalizeRepositoryPath } from "../components/source/sourceUtils";
import {
  Badge,
  Button,
  Card,
  Input,
  PageHeader,
  buttonClassName,
  surfaceClassName
} from "../components/ui";
import { cn } from "../lib/cn";
import { shortChangesetId } from "../lib/objectId";
import {
  changedPathCount,
  childCountMap,
  conflictCount,
  currentPatchset,
  currentPatchsetNumber,
  displaySubmitBlockedReason,
  entryByChangesetId,
  entryDepth,
  entryLabel,
  entryTitle,
  formatCommit,
  formatTimestamp,
  getErrorMessage,
  isTerminalChangesetStatus,
  nativeControlClassName,
  nativeTextareaClassName,
  parentEntry,
  primaryButtonClass,
  secondaryButtonClass,
  StackLoadingBlock,
  StackNotice,
  shortStackId,
  sliceRefLabel,
  sortedStackEntries,
  stackDisplayName,
  StackStatusBadge
} from "./stackPageUtils";

interface StackParams {
  id?: string;
}

interface StackSearch {
  entry?: unknown;
}

export function StackDetailPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as StackParams;
  const search = useSearch({ strict: false }) as StackSearch;
  const stackId = params.id ?? "";
  const requestedEntryId = typeof search.entry === "string" ? search.entry : "";
  const [actionMessage, setActionMessage] = useState("");
  const [actionError, setActionError] = useState("");

  const stackQuery = useQuery({
    enabled: Boolean(stackId),
    queryKey: ["stack", stackId],
    queryFn: () => api.getStack({ stackId }),
    refetchInterval: (query) =>
      hasInProgressEntry(query.state.data) ? 2500 : false
  });

  const stack = stackQuery.data;
  const entries = useMemo(() => sortedStackEntries(stack), [stack]);
  const defaultEntryId =
    stack?.activeEntryId || stack?.rootEntryId || entries[0]?.changesetId || "";
  const selectedEntry =
    entryByChangesetId(entries, requestedEntryId || defaultEntryId) ??
    entries[0] ??
    null;
  const selectedChangesetId = selectedEntry?.changesetId ?? "";

  const diffQuery = useQuery({
    enabled: Boolean(selectedChangesetId),
    queryKey: ["stackEntryDiff", stackId, selectedChangesetId],
    queryFn: () => api.diffChangeset({ changesetId: selectedChangesetId })
  });

  const invalidateStack = async () => {
    await queryClient.invalidateQueries({ queryKey: ["stack", stackId] });
  };

  const selectEntry = (entryId: string) => {
    void navigate({
      params: { id: stackId },
      search: entryId ? ({ entry: entryId } as never) : ({} as never),
      to: "/dependencies/$id"
    });
  };

  if (!stackId) {
    return <StackMessage message="No dependency tree id was provided." title="Missing dependency tree" />;
  }

  if (stackQuery.isLoading) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
        <StackLoadingBlock />
      </section>
    );
  }

  if (stackQuery.isError) {
    return (
      <StackMessage
        message={getErrorMessage(stackQuery.error)}
        title="Unable to load dependency tree"
      />
    );
  }

  if (!stack) {
    return <StackMessage message="The API returned no dependency tree." title="Dependency tree not found" />;
  }

  const sliceLabel = sliceRefLabel(stack.authoringSlice);

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="mb-4">
        <Breadcrumb
          items={[
            { label: "Dependencies", to: "/dependencies" },
            ...(sliceLabel
              ? [
                  {
                    label: sliceLabel,
                    search: { slice: sliceLabel },
                    to: "/dependencies"
                  }
                ]
              : []),
            { label: shortStackId(stack.id) || stackDisplayName(stack) }
          ]}
        />
      </div>

      <StackHeader stack={stack} />

      {actionMessage ? (
        <div className="mt-5">
          <StackNotice title="Dependencies updated" tone="success">
            {actionMessage}
          </StackNotice>
        </div>
      ) : null}
      {actionError ? (
        <div className="mt-5">
          <StackNotice title="Dependency action failed" tone="error">
            {actionError}
          </StackNotice>
        </div>
      ) : null}

      <div className="mt-6 grid gap-6 xl:grid-cols-[minmax(0,1.35fr)_minmax(24rem,0.85fr)]">
        <div className="min-w-0 space-y-6">
          <StackEntryList
            entries={entries}
            onSelect={selectEntry}
            selectedEntryId={selectedChangesetId}
            stackId={stackId}
          />
          <SelectedEntryDetail
            diffQuery={diffQuery}
            entries={entries}
            entry={selectedEntry}
            stack={stack}
          />
        </div>

        <StackTreeControls
          entries={entries}
          onError={setActionError}
          onMessage={setActionMessage}
          onRefresh={invalidateStack}
          onSelect={selectEntry}
          selectedEntry={selectedEntry}
          stack={stack}
        />
      </div>
    </section>
  );
}

function StackHeader({ stack }: { stack: ChangesetStack }) {
  const sliceLabel = sliceRefLabel(stack.authoringSlice) || "slice not returned";

  return (
    <PageHeader
      actions={
        <div className="flex flex-wrap justify-start gap-2 lg:justify-end">
          <Link
            className={secondaryButtonClass}
            params={{ id: stack.id || "" }}
            to="/dependencies/$id/update"
          >
            Update dependents
          </Link>
          <Link
            className={primaryButtonClass}
            params={{ id: stack.id || "" }}
            to="/dependencies/$id/submit"
          >
            Submit dependencies
          </Link>
        </div>
      }
      description={`${sliceLabel} on ${stack.targetRef || "target ref not returned"}`}
      eyebrow="Dependencies"
      title={<span className="font-serif">{stackDisplayName(stack)}</span>}
    />
  );
}

function StackEntryList({
  entries,
  onSelect,
  selectedEntryId,
  stackId
}: {
  entries: ChangesetStackEntry[];
  onSelect(entryId: string): void;
  selectedEntryId: string;
  stackId: string;
}) {
  const childCounts = childCountMap(entries);

  if (!entries.length) {
    return (
      <StackNotice title="No dependent changesets yet">
        Use the add changeset form to create the root changeset.
      </StackNotice>
    );
  }

  return (
    <Card level="low" padding="none">
      <div className="bg-surface-container-high px-4 py-3">
        <h2 className="text-sm font-semibold text-on-surface">Changesets</h2>
      </div>
      <div className="overflow-x-auto">
        <table className="min-w-full text-left text-sm">
          <thead className="bg-surface-container font-label text-xs font-semibold uppercase tracking-normal text-on-surface-variant">
            <tr>
              <th className="px-3 py-3 sm:px-4">Changeset</th>
              <th className="hidden px-4 py-3 sm:table-cell">State</th>
              <th className="hidden px-4 py-3 md:table-cell">Patchset</th>
              <th className="hidden px-4 py-3 lg:table-cell">Base</th>
              <th className="hidden px-4 py-3 lg:table-cell">Dependents</th>
              <th className="hidden px-4 py-3 xl:table-cell">Paths</th>
              <th className="px-3 py-3 text-right sm:px-4">Open</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((entry) => {
              const changeset = entry.changeset;
              const selected = entry.changesetId === selectedEntryId;
              const patchsetNumber = currentPatchsetNumber(changeset);
              const parent = parentEntry(entries, entry);
              const depth = Math.min(entryDepth(entry), 8);

              return (
                <tr
                  className={cn(
                    "align-top transition odd:bg-surface-container-lowest even:bg-surface-container-low hover:bg-surface-container-high",
                    selected && "bg-tertiary-container/45"
                  )}
                  key={entry.changesetId}
                >
                  <td className="max-w-[18rem] px-3 py-4 sm:max-w-none sm:px-4">
                    <button
                      className="group block w-full min-w-0 text-left"
                      onClick={() => onSelect(entry.changesetId || "")}
                      style={{ paddingLeft: `${depth * 1.25}rem` }}
                      type="button"
                    >
                      <span className="block break-words font-semibold text-on-surface underline decoration-tertiary/30 underline-offset-4 group-hover:text-primary group-hover:decoration-primary">
                        {entryLabel(entry)}
                      </span>
                      <span className="mt-1 block break-words text-sm text-on-surface-variant">
                        {entryTitle(entry)}
                      </span>
                      {changeset?.submitBlockedReason ? (
                        <Badge
                          className="mt-2 max-w-full whitespace-normal leading-5"
                          variant="tertiary"
                        >
                          {displaySubmitBlockedReason(changeset.submitBlockedReason)}
                        </Badge>
                      ) : null}
                    </button>
                  </td>
                  <td className="hidden px-4 py-4 sm:table-cell">
                    <StackStatusBadge status={entry.state || changeset?.status} />
                  </td>
                  <td className="hidden px-4 py-4 text-on-surface-variant md:table-cell">
                    {patchsetNumber ? `patchset ${patchsetNumber}` : "none"}
                  </td>
                  <td className="hidden px-4 py-4 text-on-surface-variant lg:table-cell">
                    {parent ? entryLabel(parent) : "root"}
                  </td>
                  <td className="hidden px-4 py-4 text-on-surface-variant lg:table-cell">
                    {childCounts.get(entry.changesetId || "") ?? 0}
                  </td>
                  <td className="hidden px-4 py-4 text-on-surface-variant xl:table-cell">
                    {changedPathCount(changeset)}
                  </td>
                  <td className="px-3 py-4 sm:px-4">
                    <div className="flex flex-wrap justify-end gap-2">
                      <Link
                        className={buttonClassName({
                          className: "h-8 px-3 text-xs",
                          variant: "secondary"
                        })}
                        params={{
                          id:
                            shortChangesetId(entry.changesetId || "") ||
                            entry.changesetId ||
                            ""
                        }}
                        search={{ dependency: stackId } as never}
                        to="/cs/$id"
                      >
                        Detail
                      </Link>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function SelectedEntryDetail({
  diffQuery,
  entries,
  entry,
  stack
}: {
  diffQuery: UseQueryResult<DiffChangesetResponse, Error>;
  entries: ChangesetStackEntry[];
  entry: ChangesetStackEntry | null;
  stack: ChangesetStack;
}) {
  if (!entry) {
    return (
      <StackNotice title="Select a changeset">
        Choose a changeset in the dependency tree to inspect metadata and diff.
      </StackNotice>
    );
  }

  const changeset = entry.changeset;
  const parent = parentEntry(entries, entry);
  const patchset = currentPatchset(changeset);
  const changesetUrlId =
    shortChangesetId(entry.changesetId || "") || entry.changesetId || "";

  return (
    <div className="space-y-4">
      <Card level="low" padding="md">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="break-words text-lg font-semibold text-on-surface">
                {entryTitle(entry)}
              </h2>
              <StackStatusBadge status={entry.state || changeset?.status} />
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-2 text-sm text-on-surface-variant">
              <Badge className="break-all font-mono" variant="neutral">
                {entryLabel(entry)}
              </Badge>
              <span>{changeset?.author || "author not returned"}</span>
              <span>{patchset ? `patchset ${patchset.number}` : "no patchset"}</span>
            </div>
          </div>
          <Link
            className={secondaryButtonClass}
            params={{ id: changesetUrlId }}
            search={{ dependency: stack.id } as never}
            to="/cs/$id"
          >
            Open changeset
          </Link>
        </div>

        <dl className="mt-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <Metadata label="Base changeset" value={parent ? entryLabel(parent) : "root"} />
          <Metadata
            label="Base patchset"
            value={entry.parentPatchsetId || changeset?.parentPatchsetId || "none"}
          />
          <Metadata label="Depth" value={String(entryDepth(entry))} />
          <Metadata label="Changed paths" value={String(changedPathCount(changeset))} />
          <Metadata label="Base kind" value={changeset?.baseKind || patchset?.baseKind || "commit"} />
          <Metadata label="Base commit" value={formatCommit(changeset?.baseCommitId)} />
          <Metadata label="Conflicts" value={String(conflictCount(changeset))} />
          <Metadata
            label="Updated"
            value={formatTimestamp(stack.updatedAt || stack.createdAt)}
          />
        </dl>

        {changeset?.submitBlockedReason ? (
          <div className="mt-5 rounded-sm bg-tertiary-container px-3 py-2 text-sm text-tertiary">
            {displaySubmitBlockedReason(changeset.submitBlockedReason)}
          </div>
        ) : null}
      </Card>

      <EntryPreviewTree entry={entry} stack={stack} />

      <DiffViewer
        diffResponse={diffQuery.data}
        error={diffQuery.error}
        isError={diffQuery.isError}
        isLoading={diffQuery.isPending}
      />
    </div>
  );
}

function EntryPreviewTree({
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
      <StackNotice title="No preview tree">
        Add a patchset to this entry before browsing its materialized tree.
      </StackNotice>
    );
  }

  const preview = previewQuery.data;

  return (
    <Card level="low" padding="none">
      <div className="bg-surface-container-high px-4 py-3">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <h2 className="text-sm font-semibold text-on-surface">Preview tree</h2>
            <p className="mt-1 text-xs text-on-surface-muted">
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
            <Input
              onChange={(event) => setPath(event.target.value)}
              placeholder="/acme/payment"
              value={path}
            />
            <Button type="submit" variant="secondary">
              Browse
            </Button>
          </form>
        </div>
      </div>

      {previewQuery.isPending ? (
        <div className="px-4 py-6 text-sm text-on-surface-variant">Loading preview...</div>
      ) : null}
      {previewQuery.isError ? (
        <div className="px-4 py-4">
          <StackNotice title="Preview unavailable" tone="error">
            {getErrorMessage(previewQuery.error)}
          </StackNotice>
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
        <pre className="max-h-96 overflow-auto bg-on-surface px-4 py-4 text-sm leading-6 text-on-surface-inverse">
          <code>{preview.fileText}</code>
        </pre>
      ) : null}
    </Card>
  );
}

function PreviewDirectory({
  entries,
  onOpen,
  path
}: {
  entries: TreeEntry[];
  onOpen(path: string): void;
  path: string;
}) {
  const parentPath = parentRepositoryPath(path);

  return (
    <div>
      {path !== "/" ? (
        <button
          className="flex w-full items-center justify-between bg-surface-container px-4 py-3 text-left text-sm font-medium text-on-surface-variant transition hover:bg-surface-container-high"
          onClick={() => onOpen(parentPath)}
          type="button"
        >
          <span>Parent directory</span>
          <span className="font-mono text-xs text-on-surface-muted">{parentPath}</span>
        </button>
      ) : null}
      {entries.length ? (
        entries.map((entry) => (
          <button
            className="flex w-full items-center justify-between gap-4 px-4 py-3 text-left text-sm transition odd:bg-surface-container-lowest even:bg-surface-container-low hover:bg-surface-container-high"
            key={entry.path || entry.name}
            onClick={() => onOpen(entry.path || "/")}
            type="button"
          >
            <span className="min-w-0 break-all font-medium text-on-surface">
              {entry.name || entry.path || "entry"}
            </span>
            <Badge className="shrink-0" variant="neutral">
              {entry.kind === "ENTRY_KIND_FILE" ? "file" : "directory"}
            </Badge>
          </button>
        ))
      ) : (
        <div className="px-4 py-6 text-sm text-on-surface-variant">
          This preview directory is empty.
        </div>
      )}
    </div>
  );
}

function StackTreeControls({
  entries,
  onError,
  onMessage,
  onRefresh,
  onSelect,
  selectedEntry,
  stack
}: {
  entries: ChangesetStackEntry[];
  onError(message: string): void;
  onMessage(message: string): void;
  onRefresh(): Promise<void>;
  onSelect(entryId: string): void;
  selectedEntry: ChangesetStackEntry | null;
  stack: ChangesetStack;
}) {
  return (
    <aside className="grid content-start gap-4">
      <StackMetadataPanel stack={stack} />
      <AddPatchsetPanel
        entries={entries}
        onError={onError}
        onMessage={onMessage}
        onRefresh={onRefresh}
        selectedEntry={selectedEntry}
        stack={stack}
      />
      <AddEntryPanel
        entries={entries}
        onError={onError}
        onMessage={onMessage}
        onRefresh={onRefresh}
        onSelect={onSelect}
        selectedEntry={selectedEntry}
        stackId={stack.id || ""}
      />
      <MoveEntryPanel
        entries={entries}
        onError={onError}
        onMessage={onMessage}
        onRefresh={onRefresh}
        onSelect={onSelect}
        selectedEntry={selectedEntry}
        stackId={stack.id || ""}
      />
    </aside>
  );
}

function StackMetadataPanel({ stack }: { stack: ChangesetStack }) {
  return (
    <Card level="low" padding="md">
      <h2 className="text-sm font-semibold text-on-surface">Dependency metadata</h2>
      <dl className="mt-4 grid gap-4">
        <Metadata label="Status" value={<StackStatusBadge status={stack.status} />} />
        <Metadata label="Dependency id" value={stack.id || "not returned"} />
        <Metadata label="Base" value={formatCommit(stack.baseCommitId)} />
        <Metadata label="Target" value={stack.targetRef || "not returned"} />
        <Metadata label="Created" value={formatTimestamp(stack.createdAt)} />
        <Metadata label="Updated" value={formatTimestamp(stack.updatedAt)} />
      </dl>
    </Card>
  );
}

function AddPatchsetPanel({
  entries,
  onError,
  onMessage,
  onRefresh,
  selectedEntry,
  stack
}: {
  entries: ChangesetStackEntry[];
  onError(message: string): void;
  onMessage(message: string): void;
  onRefresh(): Promise<void>;
  selectedEntry: ChangesetStackEntry | null;
  stack: ChangesetStack;
}) {
  const api = useApi();
  const [filePath, setFilePath] = useState("");
  const [fileContent, setFileContent] = useState("");

  const patchsetMutation = useMutation({
    mutationFn: async () => {
      if (!selectedEntry?.changesetId || !selectedEntry.changeset) {
        throw new Error("Choose a changeset before adding a patchset.");
      }
      if (!stack.authoringSlice?.account || !stack.authoringSlice.slice) {
        throw new Error("This dependency tree did not return an authoring slice.");
      }

      const trimmedPath = filePath.trim();
      if (!trimmedPath) {
        throw new Error("Enter a repository path.");
      }

      const current = currentPatchset(selectedEntry.changeset);
      const parent = selectedEntry.parentChangesetId
        ? entryByChangesetId(entries, selectedEntry.parentChangesetId)
        : null;
      const parentPatchset = parent ? currentPatchset(parent.changeset) : null;
      if (selectedEntry.parentChangesetId && !parentPatchset?.id) {
        throw new Error("The selected changeset's base has no current patchset.");
      }

      const uploaded = await api.uploadBlob({
        data: utf8ToBase64(fileContent),
        slice: stack.authoringSlice
      });
      if (!uploaded.blobId || !uploaded.contentHash) {
        throw new Error("UploadBlob did not return blob metadata.");
      }

      return api.updateChangeset({
        baseCommitId: selectedEntry.changeset.baseCommitId || stack.baseCommitId,
        baseKind: parentPatchset?.id ? "patchset" : "commit",
        basePatchsetId: parentPatchset?.id || "",
        changesetId: selectedEntry.changesetId,
        expectedCurrentPatchsetId: current?.id || "",
        expectedParentPatchsetId: parentPatchset?.id || "",
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
    },
    onError: (error) => onError(getErrorMessage(error)),
    onMutate: () => {
      onError("");
      onMessage("");
    },
    onSuccess: async (patchset) => {
      setFilePath("");
      setFileContent("");
      onMessage(
        patchset.number
          ? `Patchset ${patchset.number} added.`
          : "Patchset added."
      );
      await onRefresh();
    }
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    patchsetMutation.mutate();
  };

  return (
    <form
      className={cn(surfaceClassName({ level: "low" }), "p-5")}
      onSubmit={submit}
    >
      <h2 className="text-sm font-semibold text-on-surface">Add patchset</h2>
      <p className="mt-1 text-sm text-on-surface-variant">
        Revise the selected changeset without creating a dependent changeset.
      </p>
      <label className="mt-4 grid gap-2 font-label text-sm font-semibold text-on-surface">
        Selected changeset
        <Input
          disabled
          value={
            selectedEntry
              ? `${entryLabel(selectedEntry)} patchset ${currentPatchsetNumber(selectedEntry.changeset)}`
              : "Choose a changeset"
          }
        />
      </label>
      <label className="mt-4 grid gap-2 font-label text-sm font-semibold text-on-surface">
        Path
        <Input
          onChange={(event) => setFilePath(event.target.value)}
          placeholder="/acme/payment/parser.go"
          value={filePath}
        />
      </label>
      <label className="mt-4 grid gap-2 font-label text-sm font-semibold text-on-surface">
        Content
        <textarea
          className={cn(nativeTextareaClassName, "min-h-36 font-mono")}
          onChange={(event) => setFileContent(event.target.value)}
          placeholder="package payment"
          value={fileContent}
        />
      </label>
      <Button
        className="mt-5 w-full"
        disabled={patchsetMutation.isPending || !selectedEntry?.changesetId}
        type="submit"
        variant="secondary"
      >
        {patchsetMutation.isPending ? "Adding..." : "Add patchset"}
      </Button>
    </form>
  );
}

function AddEntryPanel({
  entries,
  onError,
  onMessage,
  onRefresh,
  onSelect,
  selectedEntry,
  stackId
}: {
  entries: ChangesetStackEntry[];
  onError(message: string): void;
  onMessage(message: string): void;
  onRefresh(): Promise<void>;
  onSelect(entryId: string): void;
  selectedEntry: ChangesetStackEntry | null;
  stackId: string;
}) {
  const api = useApi();
  const [mode, setMode] = useState<"child" | "sibling">("child");
  const [parentId, setParentId] = useState(selectedEntry?.changesetId || "");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");

  useEffect(() => {
    if (!selectedEntry?.changesetId) {
      return;
    }
    if (mode === "child") {
      setParentId(selectedEntry.changesetId);
    } else {
      setParentId(selectedEntry.parentChangesetId || "");
    }
  }, [mode, selectedEntry?.changesetId, selectedEntry?.parentChangesetId]);

  const addMutation = useMutation({
    mutationFn: async () => {
      if (!stackId) {
        throw new Error("This dependency tree did not return an id.");
      }
      const trimmedTitle = title.trim();
      if (!trimmedTitle) {
        throw new Error("Enter a changeset title.");
      }

      if (mode === "sibling" && selectedEntry && !selectedEntry.parentChangesetId) {
        throw new Error("The root changeset cannot have a sibling.");
      }

      const effectiveParentId =
        mode === "sibling" && selectedEntry
          ? selectedEntry.parentChangesetId || ""
          : parentId;
      const parent = effectiveParentId ? entryByChangesetId(entries, effectiveParentId) : null;
      const parentPatchset = parent ? currentPatchset(parent.changeset)?.id : "";
      if (parent && !parentPatchset) {
        throw new Error("The selected base changeset has no current patchset.");
      }

      return api.addStackEntry({
        description: description.trim(),
        parentChangesetId: parent?.changesetId || "",
        parentPatchsetId: parentPatchset,
        stackId,
        title: trimmedTitle
      });
    },
    onError: (error) => onError(getErrorMessage(error)),
    onMutate: () => {
      onError("");
      onMessage("");
    },
    onSuccess: async (changeset) => {
      setTitle("");
      setDescription("");
      onMessage("Changeset created.");
      await onRefresh();
      if (changeset.id) {
        onSelect(changeset.id);
      }
    }
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    addMutation.mutate();
  };

  return (
    <form
      className={cn(surfaceClassName({ level: "low" }), "p-5")}
      onSubmit={submit}
    >
      <h2 className="text-sm font-semibold text-on-surface">Add changeset</h2>
      <div className="mt-4 grid grid-cols-2 rounded-sm bg-surface-container-high p-1">
        {(["child", "sibling"] as const).map((option) => (
          <button
            className={cn(
              "rounded-sm px-3 py-1.5 font-label text-sm font-semibold transition",
              mode === option
                ? "bg-surface-container-lowest text-primary"
                : "text-on-surface-variant hover:bg-surface-container-highest hover:text-primary"
            )}
            key={option}
            onClick={() => setMode(option)}
            type="button"
          >
            {option === "child" ? "Dependent" : "Sibling"}
          </button>
        ))}
      </div>
      <label className="mt-4 grid gap-2 font-label text-sm font-semibold text-on-surface">
        Base changeset
        <select
          className={nativeControlClassName}
          disabled={!entries.length || mode === "sibling"}
          onChange={(event) => setParentId(event.target.value)}
          value={parentId}
        >
          <option disabled={entries.length > 0 && mode === "child"} value="">
            Root changeset
          </option>
          {entries.map((entry) => (
            <option key={entry.changesetId} value={entry.changesetId}>
              {entryLabel(entry)}
            </option>
          ))}
        </select>
      </label>
      <label className="mt-4 grid gap-2 font-label text-sm font-semibold text-on-surface">
        Title
        <Input
          onChange={(event) => setTitle(event.target.value)}
          placeholder="Use parser in API"
          value={title}
        />
      </label>
      <label className="mt-4 grid gap-2 font-label text-sm font-semibold text-on-surface">
        Description
        <textarea
          className={cn(nativeTextareaClassName, "min-h-24")}
          onChange={(event) => setDescription(event.target.value)}
          placeholder="Optional review context"
          value={description}
        />
      </label>
      <Button
        className="mt-5 w-full"
        disabled={addMutation.isPending}
        type="submit"
        variant="secondary"
      >
        {addMutation.isPending ? "Adding..." : mode === "child" ? "Add dependent" : "Add sibling"}
      </Button>
    </form>
  );
}

function MoveEntryPanel({
  entries,
  onError,
  onMessage,
  onRefresh,
  onSelect,
  selectedEntry,
  stackId
}: {
  entries: ChangesetStackEntry[];
  onError(message: string): void;
  onMessage(message: string): void;
  onRefresh(): Promise<void>;
  onSelect(entryId: string): void;
  selectedEntry: ChangesetStackEntry | null;
  stackId: string;
}) {
  const api = useApi();
  const [changesetId, setChangesetId] = useState(selectedEntry?.changesetId || "");
  const [parentId, setParentId] = useState("");
  const [siblingOrder, setSiblingOrder] = useState("");

  useEffect(() => {
    if (selectedEntry?.changesetId) {
      setChangesetId(selectedEntry.changesetId);
    }
  }, [selectedEntry?.changesetId]);

  const moveMutation = useMutation({
    mutationFn: async () => {
      if (!stackId) {
        throw new Error("This dependency tree did not return an id.");
      }
      if (!changesetId) {
        throw new Error("Choose a changeset to move.");
      }
      const parent = parentId ? entryByChangesetId(entries, parentId) : null;
      if (parent?.changesetId === changesetId) {
        throw new Error("A changeset cannot be based on itself.");
      }
      const parentPatchsetId = parent ? currentPatchset(parent.changeset)?.id : "";
      if (parent && !parentPatchsetId) {
        throw new Error("The new base has no current patchset.");
      }

      await api.reparentStackEntry({
        changesetId,
        newParentChangesetId: parent?.changesetId || "",
        newParentPatchsetId: parentPatchsetId,
        siblingOrder: siblingOrder.trim(),
        stackId
      });

      return api.restack({
        stackId,
        startChangesetId: changesetId
      });
    },
    onError: (error) => onError(getErrorMessage(error)),
    onMutate: () => {
      onError("");
      onMessage("");
    },
    onSuccess: async (result) => {
      const count = result.entries?.length ?? 0;
      onMessage(
        count
          ? `Changeset moved and ${count} dependents updated.`
          : "Changeset moved. No dependent patchsets needed updates."
      );
      await onRefresh();
      onSelect(changesetId);
    }
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    moveMutation.mutate();
  };

  return (
    <form
      className={cn(surfaceClassName({ level: "low" }), "p-5")}
      onSubmit={submit}
    >
      <h2 className="text-sm font-semibold text-on-surface">Move to base</h2>
      <label className="mt-4 grid gap-2 font-label text-sm font-semibold text-on-surface">
        Changeset
        <select
          className={nativeControlClassName}
          onChange={(event) => setChangesetId(event.target.value)}
          value={changesetId}
        >
          {entries.map((entry) => (
            <option key={entry.changesetId} value={entry.changesetId}>
              {entryLabel(entry)}
            </option>
          ))}
        </select>
      </label>
      <label className="mt-4 grid gap-2 font-label text-sm font-semibold text-on-surface">
        New base
        <select
          className={nativeControlClassName}
          onChange={(event) => setParentId(event.target.value)}
          value={parentId}
        >
          <option value="">Root</option>
          {entries
            .filter((entry) => entry.changesetId !== changesetId)
            .map((entry) => (
              <option key={entry.changesetId} value={entry.changesetId}>
                {entryLabel(entry)}
              </option>
            ))}
        </select>
      </label>
      <label className="mt-4 grid gap-2 font-label text-sm font-semibold text-on-surface">
        Sibling order
        <Input
          inputMode="numeric"
          onChange={(event) => setSiblingOrder(event.target.value)}
          placeholder="Append"
          value={siblingOrder}
        />
      </label>
      <Button
        className="mt-5 w-full"
        disabled={moveMutation.isPending || entries.length === 0}
        type="submit"
        variant="secondary"
      >
        {moveMutation.isPending ? "Moving..." : "Move and update"}
      </Button>
    </form>
  );
}

function Metadata({
  label,
  value
}: {
  label: string;
  value: ReactNode;
}) {
  return (
    <div className="min-w-0">
      <dt className="font-label text-xs font-semibold uppercase tracking-normal text-on-surface-muted">
        {label}
      </dt>
      <dd className="mt-1 min-w-0 break-all text-sm font-medium text-on-surface">
        {value}
      </dd>
    </div>
  );
}

function StackMessage({ message, title }: { message: string; title: string }) {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <Card level="low" padding="lg">
        <h1 className="font-serif text-xl font-semibold tracking-normal text-on-surface">
          {title}
        </h1>
        <p className="mt-2 text-sm text-on-surface-variant">{message}</p>
      </Card>
    </section>
  );
}

function hasInProgressEntry(stack?: ChangesetStack) {
  return Boolean(
    stack?.entries?.some((entry) => {
      const status = entry.changeset?.status;
      return status === "pending_publish" || !isTerminalChangesetStatus(status);
    })
  );
}

function utf8ToBase64(value: string) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (let index = 0; index < bytes.length; index += 1) {
    binary += String.fromCharCode(bytes[index]);
  }
  return window.btoa(binary);
}

function base64ToUtf8(value: string) {
  const binary = window.atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return new TextDecoder().decode(bytes);
}

function parentRepositoryPath(value: string) {
  const normalized = normalizeRepositoryPath(value);
  if (normalized === "/") {
    return "/";
  }
  const parts = normalized.replace(/^\/+/, "").split("/");
  parts.pop();
  return parts.length ? `/${parts.join("/")}` : "/";
}
