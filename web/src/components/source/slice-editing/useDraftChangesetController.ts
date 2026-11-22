import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useReducer, useRef } from "react";

import type { Changeset, SliceRef } from "../../../api/types";
import { type ApiClient } from "../../../api/useApi";
import { shortChangesetId } from "../../../lib/objectId";
import { changesetKey, changesetQuery, changesetsListKeyPrefix } from "../../../queries/changesets";
import {
  currentChangesetPatchset,
  errorMessageFromUnknown,
  fileEditToPendingEdit,
  preparePendingFileEdits
} from "./draftChangesetHelpers";
import {
  removePendingEdit,
  upsertPendingEdit,
  type PendingEdit
} from "./pendingEdits";

export type DraftSaveStatus = "idle" | "adopting" | "saving" | "saved" | "failed";
export type DraftActionStatus = "idle" | "submitting" | "discarding";

export interface DraftControllerState {
  changesetId: string;
  changesetLabel: string;
  currentPatchsetId: string;
  edits: PendingEdit[];
  saveStatus: DraftSaveStatus;
  actionStatus: DraftActionStatus;
  errorMessage: string;
}

export type DraftControllerAction =
  | { type: "reset"; saveStatus: DraftSaveStatus }
  | {
      type: "adopted";
      changesetId: string;
      currentPatchsetId: string;
      edits: PendingEdit[];
    }
  | { type: "editsUpdated"; edits: PendingEdit[] }
  | { type: "saveStarted" }
  | { type: "changesetCreated"; changesetId: string; currentPatchsetId: string }
  | { type: "patchsetSaved"; currentPatchsetId: string }
  | { type: "saveIdle" }
  | { type: "saveFailed"; errorMessage: string }
  | { type: "actionStatus"; actionStatus: DraftActionStatus };

export const INITIAL_DRAFT_STATE: DraftControllerState = {
  changesetId: "",
  changesetLabel: "",
  currentPatchsetId: "",
  edits: [],
  saveStatus: "idle",
  actionStatus: "idle",
  errorMessage: ""
};

export function draftControllerReducer(
  state: DraftControllerState,
  action: DraftControllerAction
): DraftControllerState {
  switch (action.type) {
    case "reset":
      return {
        ...INITIAL_DRAFT_STATE,
        saveStatus: action.saveStatus
      };
    case "adopted":
      return {
        ...state,
        changesetId: action.changesetId,
        changesetLabel: shortChangesetId(action.changesetId),
        currentPatchsetId: action.currentPatchsetId,
        edits: action.edits,
        saveStatus: "saved",
        errorMessage: ""
      };
    case "editsUpdated":
      return {
        ...state,
        edits: action.edits
      };
    case "saveStarted":
      return {
        ...state,
        saveStatus: "saving",
        errorMessage: ""
      };
    case "changesetCreated":
      return {
        ...state,
        changesetId: action.changesetId,
        changesetLabel: shortChangesetId(action.changesetId),
        currentPatchsetId: action.currentPatchsetId
      };
    case "patchsetSaved":
      return {
        ...state,
        currentPatchsetId: action.currentPatchsetId,
        saveStatus: "saved",
        errorMessage: ""
      };
    case "saveIdle":
      return {
        ...state,
        saveStatus: "idle",
        errorMessage: ""
      };
    case "saveFailed":
      return {
        ...state,
        saveStatus: "failed",
        errorMessage: action.errorMessage
      };
    case "actionStatus":
      return {
        ...state,
        actionStatus: action.actionStatus
      };
  }
}

interface DraftChangesetControllerOptions {
  api: ApiClient;
  commitId: string;
  sliceLabel: string;
  sliceRef: SliceRef | undefined;
  authorUsername: string;
}

export interface DraftChangesetController {
  actionStatus: DraftActionStatus;
  changesetId: string;
  changesetLabel: string;
  currentPatchsetId: string;
  edits: PendingEdit[];
  errorMessage: string;
  removeEdit(key: string): void;
  retrySave(): void;
  saveStatus: DraftSaveStatus;
  stageEdit(edit: PendingEdit): void;
  stageEditAndPersist(edit: PendingEdit): Promise<string>;
  discardDraft(): Promise<void>;
  submitDraft(): Promise<string>;
}

interface QueueSession {
  generation: number;
  state: DraftControllerState;
  tail: Promise<void>;
}

export function useDraftChangesetController({
  api,
  commitId,
  sliceLabel,
  sliceRef,
  authorUsername
}: DraftChangesetControllerOptions): DraftChangesetController {
  const queryClient = useQueryClient();
  const defaultTitle = `Web edits to ${sliceLabel}`;
  const [state, rawDispatch] = useReducer(
    draftControllerReducer,
    INITIAL_DRAFT_STATE
  );

  // Single authoritative session box keeping queued async commands in lockstep
  // with the reducer state, eliminating separate state/ref mirrors.
  const sessionRef = useRef<QueueSession>({
    generation: 0,
    state: INITIAL_DRAFT_STATE,
    tail: Promise.resolve()
  });

  const dispatch = useCallback((action: DraftControllerAction) => {
    sessionRef.current.state = draftControllerReducer(
      sessionRef.current.state,
      action
    );
    rawDispatch(action);
  }, []);

  const sliceAccount = sliceRef?.account ?? "";
  const sliceName = sliceRef?.slice ?? "";

  const syncChangesetCache = useCallback(
    (changesetId: string, fullChangeset?: Changeset) => {
      if (changesetId) {
        if (fullChangeset) {
          queryClient.setQueryData(changesetKey(changesetId), fullChangeset);
        } else {
          void queryClient.invalidateQueries({
            queryKey: changesetKey(changesetId)
          });
        }
      }
      if (sliceAccount && sliceName) {
        void queryClient.invalidateQueries({
          queryKey: changesetsListKeyPrefix({ account: sliceAccount, slice: sliceName })
        });
      }
    },
    [queryClient, sliceAccount, sliceName]
  );

  useEffect(() => {
    const generation = sessionRef.current.generation + 1;
    sessionRef.current.generation = generation;

    const canAdopt = Boolean(
      commitId && sliceAccount && sliceName && authorUsername
    );
    dispatch({ type: "reset", saveStatus: canAdopt ? "adopting" : "idle" });

    if (!canAdopt) {
      return;
    }

    const targetSlice: SliceRef = { account: sliceAccount, slice: sliceName };
    let cancelled = false;

    async function adoptDraft() {
      try {
        const listed = await api.listChangesets({
          authoringSlice: targetSlice,
          status: "draft",
          limit: 50,
          summary: true
        });
        const draft = (listed.changesets ?? []).find(
          (cs) => cs.author === authorUsername
        );

        if (cancelled || sessionRef.current.generation !== generation) {
          return;
        }

        if (!draft?.id) {
          dispatch({ type: "saveIdle" });
          return;
        }

        const fullDraft = await queryClient.fetchQuery({
          ...changesetQuery(api, draft.id),
          staleTime: 0
        });

        if (cancelled || sessionRef.current.generation !== generation) {
          return;
        }

        const currentPatchset = currentChangesetPatchset(fullDraft);
        const hydratedEdits = (currentPatchset?.fileEdits ?? [])
          .map(fileEditToPendingEdit)
          .filter((edit): edit is PendingEdit => Boolean(edit));

        dispatch({
          type: "adopted",
          changesetId: fullDraft.id ?? draft.id,
          currentPatchsetId:
            currentPatchset?.id ?? fullDraft.currentPatchsetId ?? "",
          edits: hydratedEdits
        });
      } catch (error) {
        if (cancelled || sessionRef.current.generation !== generation) {
          return;
        }
        dispatch({
          type: "saveFailed",
          errorMessage: errorMessageFromUnknown(error)
        });
      }
    }

    void adoptDraft();

    return () => {
      cancelled = true;
    };
  }, [api, authorUsername, commitId, dispatch, queryClient, sliceAccount, sliceName]);

  const persistEdits = useCallback(
    async (nextEdits: PendingEdit[], generation: number) => {
      if (!commitId) {
        throw new Error("Latest global state did not return a commit id.");
      }
      if (!sliceAccount || !sliceName) {
        throw new Error("Authoring slice is missing account or slice.");
      }

      const targetSlice: SliceRef = { account: sliceAccount, slice: sliceName };
      dispatch({ type: "saveStarted" });

      let draftId = sessionRef.current.state.changesetId;
      let draftPatchsetId = sessionRef.current.state.currentPatchsetId;

      if (!draftId) {
        if (!nextEdits.length) {
          dispatch({ type: "saveIdle" });
          return;
        }

        const created = await api.createChangeset({
          authoringSlice: targetSlice,
          baseCommitId: commitId,
          title: defaultTitle,
          description: ""
        });

        if (!created.id) {
          throw new Error("CreateChangeset did not return a changeset id.");
        }
        if (sessionRef.current.generation !== generation) {
          return;
        }

        draftId = created.id;
        draftPatchsetId = created.currentPatchsetId ?? "";
        dispatch({
          type: "changesetCreated",
          changesetId: draftId,
          currentPatchsetId: draftPatchsetId
        });
        syncChangesetCache(draftId, created);
      }

      const fileEdits = await preparePendingFileEdits(api, nextEdits, targetSlice);
      const patchset = await api.updateChangeset({
        changesetId: draftId,
        expectedCurrentPatchsetId: draftPatchsetId,
        baseCommitId: commitId,
        fileEdits
      });

      if (sessionRef.current.generation !== generation) {
        return;
      }
      if (!patchset.id) {
        throw new Error("UpdateChangeset did not return a patchset id.");
      }

      dispatch({ type: "patchsetSaved", currentPatchsetId: patchset.id });
      syncChangesetCache(draftId);
    },
    [api, commitId, defaultTitle, dispatch, sliceAccount, sliceName, syncChangesetCache]
  );

  const enqueueFlush = useCallback(
    (nextEdits: PendingEdit[]) => {
      const generation = sessionRef.current.generation;
      const run = sessionRef.current.tail.then(
        () => persistEdits(nextEdits, generation),
        () => persistEdits(nextEdits, generation)
      );

      sessionRef.current.tail = run.catch(() => undefined);
      void run.catch((error) => {
        if (sessionRef.current.generation !== generation) {
          return;
        }
        dispatch({
          type: "saveFailed",
          errorMessage: errorMessageFromUnknown(error)
        });
      });
    },
    [dispatch, persistEdits]
  );

  const applyEdits = useCallback(
    (nextEdits: PendingEdit[]) => {
      dispatch({ type: "editsUpdated", edits: nextEdits });
      enqueueFlush(nextEdits);
    },
    [dispatch, enqueueFlush]
  );

  const stageEdit = useCallback(
    (edit: PendingEdit) => {
      applyEdits(upsertPendingEdit(sessionRef.current.state.edits, edit));
    },
    [applyEdits]
  );

  const removeEdit = useCallback(
    (key: string) => {
      applyEdits(removePendingEdit(sessionRef.current.state.edits, key));
    },
    [applyEdits]
  );

  const retrySave = useCallback(() => {
    enqueueFlush(sessionRef.current.state.edits);
  }, [enqueueFlush]);

  const stageEditAndPersist = useCallback(
    async (edit: PendingEdit): Promise<string> => {
      stageEdit(edit);
      await sessionRef.current.tail;
      const latest = sessionRef.current.state;
      if (latest.errorMessage) {
        throw new Error(latest.errorMessage);
      }
      return latest.changesetId;
    },
    [stageEdit]
  );

  const submitDraft = useCallback(async () => {
    dispatch({ type: "actionStatus", actionStatus: "submitting" });
    try {
      await sessionRef.current.tail;
      const latest = sessionRef.current.state;
      if (latest.errorMessage) {
        throw new Error("Save failed. Retry the draft before submitting.");
      }
      if (!latest.changesetId) {
        throw new Error("There is no draft changeset to submit.");
      }
      if (!latest.currentPatchsetId) {
        throw new Error("The draft changeset has no patchset to submit.");
      }

      const id = latest.changesetId;
      await api.submitChangeset({
        changesetId: id,
        expectedCurrentPatchsetId: latest.currentPatchsetId
      });

      dispatch({ type: "reset", saveStatus: "idle" });
      syncChangesetCache(id);
      return id;
    } finally {
      dispatch({ type: "actionStatus", actionStatus: "idle" });
    }
  }, [api, dispatch, syncChangesetCache]);

  const discardDraft = useCallback(async () => {
    dispatch({ type: "actionStatus", actionStatus: "discarding" });
    try {
      await sessionRef.current.tail;
      const id = sessionRef.current.state.changesetId;
      if (id) {
        await api.abandonChangeset({
          changesetId: id,
          reason: "discarded from web"
        });
      }
      dispatch({ type: "reset", saveStatus: "idle" });
      if (id) {
        syncChangesetCache(id);
      }
    } finally {
      dispatch({ type: "actionStatus", actionStatus: "idle" });
    }
  }, [api, dispatch, syncChangesetCache]);

  return {
    actionStatus: state.actionStatus,
    changesetId: state.changesetId,
    changesetLabel: state.changesetLabel,
    currentPatchsetId: state.currentPatchsetId,
    edits: state.edits,
    errorMessage: state.errorMessage,
    removeEdit,
    retrySave,
    saveStatus: state.saveStatus,
    stageEdit,
    stageEditAndPersist,
    discardDraft,
    submitDraft
  };
}
