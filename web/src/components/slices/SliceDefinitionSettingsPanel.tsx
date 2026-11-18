import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent, type ReactNode } from "react";

import type { Slice, SliceDefinition } from "../../api/types";
import { useApi } from "../../api/useApi";
import { shortHash } from "../../lib/objectId";
import { sliceRefKey } from "../../queries/slices";
import {
  SliceDefinitionForm,
  toVisibilityOption,
  validateIncludedPaths,
  type VisibilityOption
} from "./SliceDefinitionForm";
import {
  SliceMetadataGrid,
  SliceNotice,
  SlicePanel,
  getErrorMessage
} from "./SlicePageParts";

export function SliceDefinitionSettingsPanel({
  canEdit,
  children,
  isReloading,
  onReload,
  routeAccount,
  routeSlice,
  slice
}: {
  canEdit: boolean;
  children?: ReactNode;
  isReloading: boolean;
  onReload: () => void;
  routeAccount: string;
  routeSlice: string;
  slice: Slice;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [visibility, setVisibility] = useState<VisibilityOption>("private");
  const [includedPaths, setIncludedPaths] = useState<string[]>([]);
  const [clientErrors, setClientErrors] = useState<string[]>([]);
  const [saveMessage, setSaveMessage] = useState<string | null>(null);
  const [conflictMessage, setConflictMessage] = useState<string | null>(null);

  useEffect(() => {
    setVisibility(toVisibilityOption(slice.definition?.visibility));
    setIncludedPaths(slice.definition?.includedPaths ?? []);
    setClientErrors([]);
  }, [slice.definitionHash, slice.id]);

  const updateMutation = useMutation({
    mutationFn: async (normalizedPaths: string[]) => {
      if (!slice.id) {
        throw new Error("Slice has no internal id.");
      }

      const definition: SliceDefinition = {
        ...(slice.definition ?? {}),
        includedPaths: normalizedPaths,
        sliceId: slice.definition?.sliceId || slice.id,
        visibility
      };

      return api.updateSliceDefinition({
        definition,
        expectedDefinitionHash: slice.definitionHash,
        sliceId: slice.id
      });
    },
    onError: (error) => {
      setSaveMessage(null);

      if (isHashConflict(error)) {
        setConflictMessage(
          "The slice definition changed on the server. The latest definition has been reloaded; review it and save again."
        );
        void queryClient.invalidateQueries({
          queryKey: sliceRefKey(routeAccount, routeSlice)
        });
      }
    },
    onSuccess: async () => {
      setConflictMessage(null);
      setSaveMessage("Definition saved.");
      await queryClient.invalidateQueries({
        queryKey: sliceRefKey(routeAccount, routeSlice)
      });
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

  return (
    <form className="mt-8 space-y-6" onSubmit={saveDefinition}>
      <SlicePanel>
        <SliceMetadataGrid
          rows={[
            {
              label: "Current hash",
              value: (
                <code className="font-mono text-xs" title={slice.definitionHash}>
                  {slice.definitionHash ? shortHash(slice.definitionHash) : "none"}
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
                <code className="font-mono text-xs">{slice.id ?? ""}</code>
              )
            },
            {
              label: "Loaded visibility",
              value: slice.definition?.visibility || "unspecified"
            }
          ]}
        />
      </SlicePanel>

      {children}

      {canEdit ? (
        <>
          <SliceDefinitionForm
            account={slice.ref?.account}
            disabled={updateMutation.isPending}
            includedPaths={includedPaths}
            includedPathsLocked={slice.ref?.slice === "home"}
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
              className="rounded-md bg-zinc-950 dark:bg-zinc-100 px-4 py-2 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-200 dark:disabled:bg-zinc-700 disabled:text-slate-500 dark:disabled:text-zinc-400"
              disabled={updateMutation.isPending}
              type="submit"
            >
              {updateMutation.isPending ? "Saving..." : "Save definition"}
            </button>
            <button
              className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-4 py-2 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
              disabled={isReloading}
              onClick={onReload}
              type="button"
            >
              Reload
            </button>
          </div>
        </>
      ) : null}
    </form>
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
