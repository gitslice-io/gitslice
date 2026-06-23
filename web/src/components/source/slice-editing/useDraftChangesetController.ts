import {
  useCallback,
  useEffect,
  useRef,
  useState
} from "react";

import type { Changeset, FileEdit, SliceRef } from "../../../api/types";
import { type ApiClient } from "../../../api/useApi";
import { shortChangesetId } from "../../../lib/objectId";

import type { PendingEdit } from "./pendingEdits";
import { upsertPendingEdit, removePendingEdit } from "./pendingEdits";
import {
  compareEditOrder,
  currentChangesetPatchset,
  draftStatusLabel,
  errorMessageFromUnknown,
  fileEditToPendingEdit,
  preparePendingFileEdits
} from "./draftChangesetHelpers";

type DraftSaveStatus = "idle" | "adopting" | "saving" | "saved" | "failed";
type DraftActionStatus = "idle" | "submitting" | "discarding";

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
  discardDraft(): Promise<void>;
  submitDraft(): Promise<string>;
}

export function useDraftChangesetController({
  api,
  commitId,
  sliceLabel,
  sliceRef,
  authorUsername
}: DraftChangesetControllerOptions): DraftChangesetController {
  const defaultTitle = `Web edits to ${sliceLabel}`;
  const [changesetId, setChangesetId] = useState("");
  const [changesetLabel, setChangesetLabel] = useState("");
  const [currentPatchsetId, setCurrentPatchsetId] = useState("");
  const [edits, setEdits] = useState<PendingEdit[]>([]);
  const [saveStatus, setSaveStatus] = useState<DraftSaveStatus>("idle");
  const [actionStatus, setActionStatus] =
    useState<DraftActionStatus>("idle");
  const [errorMessage, setErrorMessage] = useState("");

  const changesetIdRef = useRef("");
  const currentPatchsetIdRef = useRef("");
  const editsRef = useRef<PendingEdit[]>([]);
  const errorMessageRef = useRef("");
  const flushTailRef = useRef<Promise<void>>(Promise.resolve());
  const generationRef = useRef(0);

  const setControllerError = useCallback((message: string) => {
    errorMessageRef.current = message;
    setErrorMessage(message);
  }, []);

  const clearControllerError = useCallback(() => {
    errorMessageRef.current = "";
    setErrorMessage("");
  }, []);

  const setDraftIdentity = useCallback(
    (changeset: Changeset | undefined) => {
      const nextChangesetId = changeset?.id ?? "";
      const nextLabel = shortChangesetId(nextChangesetId);
      const nextPatchsetId = changeset?.currentPatchsetId ?? "";

      changesetIdRef.current = nextChangesetId;
      currentPatchsetIdRef.current = nextPatchsetId;
      setChangesetId(nextChangesetId);
      setChangesetLabel(nextLabel);
      setCurrentPatchsetId(nextPatchsetId);
    },
    []
  );

  useEffect(() => {
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    changesetIdRef.current = "";
    currentPatchsetIdRef.current = "";
    editsRef.current = [];
    setChangesetId("");
    setChangesetLabel("");
    setCurrentPatchsetId("");
    setEdits([]);
    clearControllerError();

    if (!commitId || !sliceRef?.account || !sliceRef?.slice || !authorUsername) {
      setSaveStatus("idle");
      return;
    }

    let cancelled = false;
    setSaveStatus("adopting");

    async function adoptDraft() {
      try {
        const listed = await api.listChangesets({
          authoringSlice: sliceRef,
          status: "draft",
          limit: 50
        });
        const draft = (listed.changesets ?? []).find(
          (changeset) => changeset.author === authorUsername
        );

        if (cancelled || generationRef.current !== generation) {
          return;
        }

        if (!draft?.id) {
          setSaveStatus("idle");
          return;
        }

        const fullDraft = await api.getChangeset({
          changesetId: draft.id
        });

        if (cancelled || generationRef.current !== generation) {
          return;
        }

        const currentPatchset = currentChangesetPatchset(fullDraft);
        const hydratedEdits = (currentPatchset?.fileEdits ?? [])
          .map(fileEditToPendingEdit)
          .filter((edit): edit is PendingEdit => Boolean(edit));

        setDraftIdentity(fullDraft);
        currentPatchsetIdRef.current =
          currentPatchset?.id ?? fullDraft.currentPatchsetId ?? "";
        setCurrentPatchsetId(currentPatchsetIdRef.current);
        editsRef.current = hydratedEdits;
        setEdits(hydratedEdits);
        setSaveStatus("saved");
      } catch (error) {
        if (cancelled || generationRef.current !== generation) {
          return;
        }
        setSaveStatus("failed");
        setControllerError(errorMessageFromUnknown(error));
      }
    }

    void adoptDraft();

    return () => {
      cancelled = true;
    };
  }, [api, clearControllerError, commitId, setControllerError, setDraftIdentity, sliceRef, authorUsername]);

  const clearDraftState = useCallback(() => {
    changesetIdRef.current = "";
    currentPatchsetIdRef.current = "";
    editsRef.current = [];
    setChangesetId("");
    setChangesetLabel("");
    setCurrentPatchsetId("");
    setEdits([]);
    clearControllerError();
    setSaveStatus("idle");
  }, [clearControllerError]);

  const persistEdits = useCallback(
    async (nextEdits: PendingEdit[], generation: number) => {
      if (!commitId) {
        throw new Error("Latest global state did not return a commit id.");
      }
      if (!sliceRef?.account || !sliceRef?.slice) {
        throw new Error("Authoring slice is missing account or slice.");
      }

      clearControllerError();
      setSaveStatus("saving");

      let draftId = changesetIdRef.current;
      let draftPatchsetId = currentPatchsetIdRef.current;

      if (!draftId) {
        if (!nextEdits.length) {
          setSaveStatus("idle");
          return;
        }

        const changeset = await api.createChangeset({
          authoringSlice: sliceRef,
          baseCommitId: commitId,
          title: defaultTitle,
          description: ""
        });

        if (!changeset.id) {
          throw new Error("CreateChangeset did not return a changeset id.");
        }

        if (generationRef.current !== generation) {
          return;
        }

        draftId = changeset.id;
        draftPatchsetId = changeset.currentPatchsetId ?? "";
        changesetIdRef.current = draftId;
        currentPatchsetIdRef.current = draftPatchsetId;
        setChangesetId(draftId);
        setChangesetLabel(shortChangesetId(draftId));
        setCurrentPatchsetId(draftPatchsetId);
      }

      const fileEdits = await preparePendingFileEdits(api, nextEdits, sliceRef);
      const patchset = await api.updateChangeset({
        changesetId: draftId,
        expectedCurrentPatchsetId: draftPatchsetId,
        baseCommitId: commitId,
        fileEdits
      });

      if (generationRef.current !== generation) {
        return;
      }

      if (!patchset.id) {
        throw new Error("UpdateChangeset did not return a patchset id.");
      }

      currentPatchsetIdRef.current = patchset.id ?? "";
      setCurrentPatchsetId(patchset.id ?? "");
      setSaveStatus("saved");
    },
    [api, clearControllerError, commitId, defaultTitle, sliceRef]
  );

  const queueFlush = useCallback(
    (nextEdits: PendingEdit[]) => {
      const generation = generationRef.current;
      const run = flushTailRef.current.then(
        () => persistEdits(nextEdits, generation),
        () => persistEdits(nextEdits, generation)
      );

      flushTailRef.current = run.catch(() => undefined);
      void run.catch((error) => {
        if (generationRef.current !== generation) {
          return;
        }
        setSaveStatus("failed");
        setControllerError(errorMessageFromUnknown(error));
      });
    },
    [persistEdits, setControllerError]
  );

  const applyEdits = useCallback(
    (nextEdits: PendingEdit[]) => {
      editsRef.current = nextEdits;
      setEdits(nextEdits);
      queueFlush(nextEdits);
    },
    [queueFlush]
  );

  const stageEdit = useCallback(
    (edit: PendingEdit) => {
      applyEdits(upsertPendingEdit(editsRef.current, edit));
    },
    [applyEdits]
  );

  const removeEdit = useCallback(
    (key: string) => {
      applyEdits(removePendingEdit(editsRef.current, key));
    },
    [applyEdits]
  );

  const retrySave = useCallback(() => {
    queueFlush(editsRef.current);
  }, [queueFlush]);

  const submitDraft = useCallback(async () => {
    setActionStatus("submitting");
    try {
      await flushTailRef.current;
      if (errorMessageRef.current) {
        throw new Error("Save failed. Retry the draft before submitting.");
      }

      const id = changesetIdRef.current;
      const expectedCurrentPatchsetId = currentPatchsetIdRef.current;
      if (!id) {
        throw new Error("There is no draft changeset to submit.");
      }
      if (!expectedCurrentPatchsetId) {
        throw new Error("The draft changeset has no patchset to submit.");
      }

      await api.submitChangeset({
        changesetId: id,
        expectedCurrentPatchsetId
      });

      clearDraftState();
      return id;
    } finally {
      setActionStatus("idle");
    }
  }, [api, clearDraftState]);

  const discardDraft = useCallback(async () => {
    setActionStatus("discarding");
    try {
      await flushTailRef.current;
      const id = changesetIdRef.current;
      if (id) {
        await api.abandonChangeset({
          changesetId: id,
          reason: "discarded from web"
        });
      }
      clearDraftState();
    } finally {
      setActionStatus("idle");
    }
  }, [api, clearDraftState]);

  return {
    actionStatus,
    changesetId,
    changesetLabel,
    currentPatchsetId,
    edits,
    errorMessage,
    removeEdit,
    retrySave,
    saveStatus,
    stageEdit,
    discardDraft,
    submitDraft
  };
}