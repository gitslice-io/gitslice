import { describe, expect, it, vi } from "vitest";

import type { ApiClient } from "../../../api/useApi";

import {
  fileEditToPendingEdit,
  preparePendingFileEdits
} from "./draftChangesetHelpers";

describe("copied file edits", () => {
  it("send a copied file by blob reference with its mode and no upload", async () => {
    const uploadBlob = vi.fn();
    const edits = await preparePendingFileEdits(
      { uploadBlob } as unknown as ApiClient,
      [
        {
          kind: "write",
          path: "/a/run.sh",
          isNew: true,
          blobId: "blob1",
          contentHash: "hash1",
          mode: 0o100755
        }
      ],
      { account: "nic", slice: "s" }
    );
    expect(uploadBlob).not.toHaveBeenCalled();
    expect(edits).toEqual([
      {
        op: "add",
        path: "/a/run.sh",
        blobId: "blob1",
        contentHash: "hash1",
        mode: 0o100755
      }
    ]);
  });

  it("keep their mode when a saved draft is read back", () => {
    expect(
      fileEditToPendingEdit({
        op: "add",
        path: "/a/run.sh",
        blobId: "b",
        contentHash: "h",
        mode: 0o100755
      })
    ).toMatchObject({ kind: "write", mode: 0o100755 });
  });
});
