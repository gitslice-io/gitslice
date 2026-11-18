import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ApiClient } from "../../../api/useApi";
import {
  INITIAL_DRAFT_STATE,
  draftControllerReducer,
  useDraftChangesetController
} from "./useDraftChangesetController";

function makeWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
  };
}

describe("draftControllerReducer", () => {
  it("transitions draft identity, edits, and error state deterministically", () => {
    let state = draftControllerReducer(INITIAL_DRAFT_STATE, {
      type: "saveStarted"
    });
    expect(state.saveStatus).toBe("saving");

    state = draftControllerReducer(state, {
      type: "changesetCreated",
      changesetId: "cs_abcdef123456",
      currentPatchsetId: "ps_1"
    });
    expect(state.changesetId).toBe("cs_abcdef123456");
    expect(state.changesetLabel).toBe("abcdef1234");

    state = draftControllerReducer(state, {
      type: "patchsetSaved",
      currentPatchsetId: "ps_2"
    });
    expect(state.currentPatchsetId).toBe("ps_2");
    expect(state.saveStatus).toBe("saved");

    state = draftControllerReducer(state, {
      type: "saveFailed",
      errorMessage: "conflict"
    });
    expect(state.saveStatus).toBe("failed");
    expect(state.errorMessage).toBe("conflict");
  });
});

describe("useDraftChangesetController", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("serializes rapid multi-file edits into sequential patchset updates and syncs React Query cache", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } }
    });
    let patchsetSeq = 1;
    const api = {
      listChangesets: vi.fn().mockResolvedValue({ changesets: [] }),
      createChangeset: vi.fn().mockResolvedValue({
        id: "cs_new1234567",
        currentPatchsetId: "ps_1"
      }),
      uploadBlob: vi.fn().mockResolvedValue({
        blobId: "blob_1",
        contentHash: "hash_1"
      }),
      updateChangeset: vi.fn().mockImplementation(async () => {
        patchsetSeq += 1;
        return { id: `ps_${patchsetSeq}` };
      }),
      submitChangeset: vi.fn().mockResolvedValue({})
    } as unknown as ApiClient;

    const { result } = renderHook(
      () =>
        useDraftChangesetController({
          api,
          commitId: "commit_1",
          sliceLabel: "acme:app",
          sliceRef: { account: "acme", slice: "app" },
          authorUsername: "nic"
        }),
      { wrapper: makeWrapper(queryClient) }
    );

    await waitFor(() => expect(result.current.saveStatus).toBe("idle"));

    let persistedId = "";
    await act(async () => {
      result.current.stageEdit({
        kind: "write",
        path: "src/a.ts",
        content: "export const a = 1;\n",
        isNew: true
      });
      persistedId = await result.current.stageEditAndPersist({
        kind: "write",
        path: "src/b.ts",
        content: "export const b = 2;\n",
        isNew: true
      });
    });

    expect(persistedId).toBe("cs_new1234567");
    expect(api.createChangeset).toHaveBeenCalledTimes(1);
    expect(api.updateChangeset).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.updateChangeset).mock.calls[1]?.[0]).toMatchObject({
      changesetId: "cs_new1234567",
      expectedCurrentPatchsetId: "ps_2"
    });
    expect(result.current.currentPatchsetId).toBe("ps_3");
    expect(result.current.edits).toHaveLength(2);

    let submittedId = "";
    await act(async () => {
      submittedId = await result.current.submitDraft();
    });
    expect(submittedId).toBe("cs_new1234567");
    expect(api.submitChangeset).toHaveBeenCalledWith({
      changesetId: "cs_new1234567",
      expectedCurrentPatchsetId: "ps_3"
    });
    expect(result.current.changesetId).toBe("");
    expect(result.current.edits).toEqual([]);
  });
});
