import { Link, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";

import type { SliceDefinition } from "../api/types";
import { useApi } from "../api/useApi";
import {
  SliceDefinitionForm,
  toVisibilityOption,
  validateIncludedPaths,
  type VisibilityOption
} from "../components/slices/SliceDefinitionForm";
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

export function SliceSettingsPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const params = useParams({ strict: false }) as SliceParams;
  const sliceId = params.id ?? "";
  const [visibility, setVisibility] = useState<VisibilityOption>("account");
  const [includedPaths, setIncludedPaths] = useState<string[]>([]);
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

        <SliceDefinitionForm
          disabled={updateMutation.isPending}
          includedPaths={includedPaths}
          onIncludedPathsChange={setIncludedPaths}
          onVisibilityChange={setVisibility}
          visibility={visibility}
        />

        {clientErrors.length > 0 ? (
          <SliceNotice title="Fix included paths" tone="error">
            <ul className="list-disc space-y-1 pl-5">
              {clientErrors.map((error) => (
                <li className="break-all" key={error}>{error}</li>
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
