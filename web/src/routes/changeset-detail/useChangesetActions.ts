import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import type { Changeset } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { changesetKey } from "../../queries/changesets";
import { errorMessage, mergeErrorMessage } from "./status";

export function useChangesetActions({
  api,
  changeset,
  changesetId,
  canonicalChangesetId
}: {
  api: ApiClient;
  changeset: Changeset | undefined;
  changesetId: string;
  canonicalChangesetId: string;
}) {
  const queryClient = useQueryClient();
  const [abandonReason, setAbandonReason] = useState("");
  const [actionError, setActionError] = useState("");

  const invalidateChangeset = async () => {
    await queryClient.invalidateQueries({
      queryKey: changesetKey(changesetId)
    });
  };

  const mergeMutation = useMutation({
    mutationFn: async () => {
      if (!canonicalChangesetId) {
        throw new Error("This changeset did not return an id.");
      }
      if (!changeset?.currentPatchsetId) {
        throw new Error("This changeset has no current patchset to merge.");
      }

      return api.submitChangeset({
        changesetId: canonicalChangesetId,
        expectedCurrentPatchsetId: changeset.currentPatchsetId
      });
    },
    onError: (error) => setActionError(mergeErrorMessage(error)),
    onMutate: () => setActionError(""),
    onSuccess: async () => {
      setActionError("");
      await invalidateChangeset();
    }
  });

  const saveInfo = async (title: string, description: string) => {
    if (!canonicalChangesetId) {
      throw new Error("This changeset did not return an id.");
    }
    await api.updateChangesetInfo({
      changesetId: canonicalChangesetId,
      description,
      title
    });
    await invalidateChangeset();
  };

  const approveMutation = useMutation({
    mutationFn: async () => {
      if (!canonicalChangesetId) {
        throw new Error("This changeset did not return an id.");
      }
      return api.approveChangeset({
        changesetId: canonicalChangesetId
      });
    },
    onError: (error) => setActionError(errorMessage(error)),
    onMutate: () => setActionError(""),
    onSuccess: async () => {
      setActionError("");
      await invalidateChangeset();
    }
  });

  const abandonMutation = useMutation({
    mutationFn: async () => {
      if (!canonicalChangesetId) {
        throw new Error("This changeset did not return an id.");
      }

      return api.abandonChangeset({
        changesetId: canonicalChangesetId,
        reason: abandonReason.trim()
      });
    },
    onError: (error) => setActionError(errorMessage(error)),
    onMutate: () => setActionError(""),
    onSuccess: async () => {
      setActionError("");
      setAbandonReason("");
      await invalidateChangeset();
    }
  });

  const submitAbandon = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    abandonMutation.mutate();
  };

  return {
    abandonReason,
    setAbandonReason,
    actionError,
    actionBusy:
      mergeMutation.isPending || abandonMutation.isPending || approveMutation.isPending,
    approvePending: approveMutation.isPending,
    mergePending: mergeMutation.isPending,
    abandonPending: abandonMutation.isPending,
    approve: () => approveMutation.mutate(),
    merge: () => mergeMutation.mutate(),
    submitAbandon,
    saveInfo
  };
}
