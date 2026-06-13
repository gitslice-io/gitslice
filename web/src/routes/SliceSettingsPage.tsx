import { Link, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";

import type { SliceDefinition } from "../api/types";
import { useApi } from "../api/useApi";
import {
  SliceLoadingBlock,
  SliceMetadataGrid,
  SliceNotice,
  SlicePageHeader,
  SlicePanel,
  getErrorMessage,
  sliceDisplayName
} from "../components/slices/SlicePageParts";

interface SliceParams {
  id?: string;
}

const VISIBILITY_OPTIONS = ["private", "account", "public"] as const;

type VisibilityOption = (typeof VISIBILITY_OPTIONS)[number];

export function SliceSettingsPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const params = useParams({ strict: false }) as SliceParams;
  const sliceId = params.id ?? "";
  const [visibility, setVisibility] = useState<VisibilityOption>("account");
  const [includedPaths, setIncludedPaths] = useState<string[]>([]);
  const [pathDraft, setPathDraft] = useState("");
  const [clientErrors, setClientErrors] = useState<string[]>([]);
  const [saveMessage, setSaveMessage] = useState<string | null>(null);
  const [conflictMessage, setConflictMessage] = useState<string | null>(null);

  const sliceQuery = useQuery({
    enabled: sliceId.length > 0,
    queryKey: ["slice", sliceId],
    queryFn: () => api.getSlice({ sliceId })
  });

  const slice = sliceQuery.data;

  useEffect(() => {
    if (!slice) {
      return;
    }

    setVisibility(toVisibilityOption(slice.definition?.visibility));
    setIncludedPaths(slice.definition?.includedPaths ?? []);
    setPathDraft("");
    setClientErrors([]);
  }, [slice?.definitionHash, slice?.id]);

  const updateMutation = useMutation({
    mutationFn: async (normalizedPaths: string[]) => {
      if (!slice) {
        throw new Error("Slice has not loaded yet.");
      }

      const definition: SliceDefinition = {
        ...(slice.definition ?? {}),
        includedPaths: normalizedPaths,
        sliceId: slice.definition?.sliceId || slice.id || sliceId,
        visibility
      };

      return api.updateSliceDefinition({
        definition,
        expectedDefinitionHash: slice.definitionHash,
        sliceId
      });
    },
    onError: (error) => {
      setSaveMessage(null);

      if (isHashConflict(error)) {
        setConflictMessage(
          "The slice definition changed on the server. The latest definition has been reloaded; review it and save again."
        );
        void queryClient.invalidateQueries({ queryKey: ["slice", sliceId] });
      }
    },
    onSuccess: async () => {
      setConflictMessage(null);
      setSaveMessage("Definition saved.");
      await queryClient.invalidateQueries({ queryKey: ["slice", sliceId] });
      await queryClient.invalidateQueries({ queryKey: ["slices"] });
    }
  });

  function saveDefinition(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const validation = validateIncludedPaths(includedPaths);
    setClientErrors(validation.errors);
    setSaveMessage(null);
    setConflictMessage(null);

    if (validation.errors.length > 0) {
      return;
    }

    updateMutation.mutate(validation.paths);
  }

  function addPath() {
    const nextPath = pathDraft.trim();

    if (!nextPath) {
      return;
    }

    setIncludedPaths((current) => [...current, nextPath]);
    setPathDraft("");
  }

  function updatePath(index: number, value: string) {
    setIncludedPaths((current) =>
      current.map((path, pathIndex) => (pathIndex === index ? value : path))
    );
  }

  function removePath(index: number) {
    setIncludedPaths((current) =>
      current.filter((_, pathIndex) => pathIndex !== index)
    );
  }

  if (sliceQuery.isLoading) {
    return (
      <section className="mx-auto w-full max-w-7xl">
        <SliceLoadingBlock />
      </section>
    );
  }

  if (sliceQuery.isError) {
    return (
      <section className="mx-auto w-full max-w-7xl">
        <SlicePageHeader title="Slice Settings" />
        <div className="mt-8">
          <SliceNotice title="Could not load slice" tone="error">
            {getErrorMessage(sliceQuery.error)}
          </SliceNotice>
        </div>
      </section>
    );
  }

  if (!slice) {
    return (
      <section className="mx-auto w-full max-w-7xl">
        <SlicePageHeader title="Slice Settings" />
        <div className="mt-8">
          <SliceNotice title="Slice not found">
            No slice was returned for id {sliceId || "unknown"}.
          </SliceNotice>
        </div>
      </section>
    );
  }

  return (
    <section className="mx-auto w-full max-w-7xl">
      <SlicePageHeader
        actions={
          <Link
            className="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
            params={{ id: sliceId }}
            to="/slices/$id"
          >
            Back to slice
          </Link>
        }
        title={`Slice Settings: ${sliceDisplayName(slice)}`}
        description="Edit the supported slice definition fields only."
      />

      <form className="mt-8 space-y-6" onSubmit={saveDefinition}>
        <SlicePanel>
          <SliceMetadataGrid
            rows={[
              {
                label: "Current hash",
                value: (
                  <code className="font-mono text-xs">
                    {slice.definitionHash || "none"}
                  </code>
                )
              },
              {
                label: "Version",
                value: slice.definition?.version ?? "unknown"
              },
              {
                label: "Slice id",
                value: (
                  <code className="font-mono text-xs">{slice.id || sliceId}</code>
                )
              },
              {
                label: "Loaded visibility",
                value: slice.definition?.visibility || "unspecified"
              }
            ]}
          />
        </SlicePanel>

        <SlicePanel className="space-y-4">
          <div>
            <h2 className="text-base font-semibold text-zinc-950">
              Visibility
            </h2>
            <p className="mt-1 text-sm leading-6 text-slate-600">
              Choose one of the visibility values supported by the slice
              definition.
            </p>
          </div>
          <div className="grid gap-3 sm:grid-cols-3">
            {VISIBILITY_OPTIONS.map((option) => (
              <label
                className="flex items-center gap-3 rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-sm font-medium text-zinc-950"
                key={option}
              >
                <input
                  checked={visibility === option}
                  className="h-4 w-4 accent-zinc-950"
                  name="visibility"
                  onChange={() => setVisibility(option)}
                  type="radio"
                  value={option}
                />
                {option}
              </label>
            ))}
          </div>
        </SlicePanel>

        <SlicePanel className="space-y-4">
          <div>
            <h2 className="text-base font-semibold text-zinc-950">
              Included paths
            </h2>
            <p className="mt-1 text-sm leading-6 text-slate-600">
              Paths should be account-rooted, for example /acme/payment. The
              server performs final validation.
            </p>
          </div>

          <div className="space-y-3">
            {includedPaths.length === 0 ? (
              <div className="rounded-md border border-dashed border-slate-300 bg-slate-50 p-4 text-sm text-slate-600">
                No included paths are currently set.
              </div>
            ) : (
              includedPaths.map((path, index) => (
                <div
                  className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]"
                  key={index}
                >
                  <label className="grid gap-2 text-sm font-medium text-zinc-950">
                    Path {index + 1}
                    <input
                      className="h-10 rounded-md border border-slate-300 bg-white px-3 font-mono text-sm text-zinc-950 outline-none transition focus:border-slate-500"
                      onChange={(event) =>
                        updatePath(index, event.target.value)
                      }
                      placeholder="/acme/payment"
                      spellCheck={false}
                      value={path}
                    />
                  </label>
                  <button
                    className="self-end rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
                    onClick={() => removePath(index)}
                    type="button"
                  >
                    Remove
                  </button>
                </div>
              ))
            )}
          </div>

          <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
            <label className="grid gap-2 text-sm font-medium text-zinc-950">
              Add path
              <input
                className="h-10 rounded-md border border-slate-300 bg-white px-3 font-mono text-sm text-zinc-950 outline-none transition focus:border-slate-500"
                onChange={(event) => setPathDraft(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    addPath();
                  }
                }}
                placeholder="/acme/proto/payment"
                spellCheck={false}
                value={pathDraft}
              />
            </label>
            <button
              className="self-end rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-100 disabled:text-slate-400"
              disabled={!pathDraft.trim()}
              onClick={addPath}
              type="button"
            >
              Add path
            </button>
          </div>
        </SlicePanel>

        {clientErrors.length > 0 ? (
          <SliceNotice title="Fix included paths" tone="error">
            <ul className="list-disc space-y-1 pl-5">
              {clientErrors.map((error) => (
                <li key={error}>{error}</li>
              ))}
            </ul>
          </SliceNotice>
        ) : null}

        {updateMutation.isError && !conflictMessage ? (
          <SliceNotice title="Could not save definition" tone="error">
            {getErrorMessage(updateMutation.error)}
          </SliceNotice>
        ) : null}

        {conflictMessage ? (
          <SliceNotice title="Definition hash conflict" tone="error">
            {conflictMessage}
          </SliceNotice>
        ) : null}

        {saveMessage ? (
          <SliceNotice title={saveMessage} tone="success">
            The latest slice definition is being refreshed from the server.
          </SliceNotice>
        ) : null}

        <div className="flex flex-wrap items-center gap-3">
          <button
            className="rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-200 disabled:text-slate-500"
            disabled={updateMutation.isPending}
            type="submit"
          >
            {updateMutation.isPending ? "Saving..." : "Save definition"}
          </button>
          <button
            className="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
            disabled={sliceQuery.isFetching}
            onClick={() => {
              void sliceQuery.refetch();
            }}
            type="button"
          >
            Reload
          </button>
        </div>
      </form>
    </section>
  );
}

function toVisibilityOption(value?: string): VisibilityOption {
  if (value === "private" || value === "public" || value === "account") {
    return value;
  }

  return "account";
}

function validateIncludedPaths(paths: string[]) {
  const errors: string[] = [];
  const normalizedPaths = paths.map((path) => path.trim()).filter(Boolean);
  const seen = new Set<string>();

  if (paths.some((path) => !path.trim())) {
    errors.push("Included paths cannot be blank.");
  }

  if (normalizedPaths.length === 0) {
    errors.push("At least one included path is required.");
  }

  normalizedPaths.forEach((path) => {
    if (!path.startsWith("/")) {
      errors.push(`${path} must start with /.`);
    }

    if (path === "/") {
      errors.push("/ must include an account segment.");
    }

    if (path.length > 1 && path.endsWith("/")) {
      errors.push(`${path} must not end with /.`);
    }

    if (path.includes("//")) {
      errors.push(`${path} must not contain empty path segments.`);
    }

    if (path.includes("\0")) {
      errors.push(`${path} contains an invalid null character.`);
    }

    const segments = path.split("/").filter(Boolean);
    if (segments.some((segment) => segment === "." || segment === "..")) {
      errors.push(`${path} must not contain . or .. path segments.`);
    }

    if (seen.has(path)) {
      errors.push(`${path} is duplicated.`);
    }

    seen.add(path);
  });

  return {
    errors: Array.from(new Set(errors)),
    paths: normalizedPaths
  };
}

function isHashConflict(error: unknown) {
  const maybeError = error as {
    code?: string | number;
    message?: string;
    status?: number;
  };
  const text = `${maybeError.code ?? ""} ${
    maybeError.message ?? ""
  }`.toLowerCase();

  return (
    maybeError.status === 409 ||
    text.includes("conflict") ||
    text.includes("definition hash") ||
    text.includes("expected_definition_hash") ||
    text.includes("stale")
  );
}
