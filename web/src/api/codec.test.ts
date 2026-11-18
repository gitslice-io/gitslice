import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { SetAccountAvatarRequestSchema } from "../gen/proto/core/v1/auth_pb";
import { UploadBlobChunkSchema } from "../gen/proto/core/v1/blob_pb";
import { ListChangesetsRequestSchema, CreatePathIntentRequestSchema } from "../gen/proto/core/v1/changeset_pb";
import { EntryKind } from "../gen/proto/core/v1/common_pb";
import { ListDirectoryResponseSchema, ListCommitsRequestSchema } from "../gen/proto/core/v1/repository_pb";
import { GetConversationEventsRequestSchema } from "../gen/proto/core/v1/agent_pb";
import { decodeMessage, encodeMessage } from "./codec";

// The codec reads what each field is from the descriptors, never from its name.

describe("encodeMessage", () => {
  it("makes int64 fields bigint and leaves a same-named int32 as a number", () => {
    // limit: int32 here, int64 there.
    expect(encodeMessage(ListCommitsRequestSchema, { limit: 50 })).toEqual({ limit: 50 });
    expect(encodeMessage(GetConversationEventsRequestSchema, { limit: 200, afterSeq: "7" })).toEqual({
      limit: 200n,
      afterSeq: 7n
    });
    expect(encodeMessage(CreatePathIntentRequestSchema, { ttlSeconds: 3600.9, pattern: "a/**" })).toEqual({
      ttlSeconds: 3600n,
      pattern: "a/**"
    });
  });

  it("decodes base64 into bytes for any bytes field, not only one named data", () => {
    expect(encodeMessage(SetAccountAvatarRequestSchema, { account: "nic", image: "AQID" })).toEqual({
      account: "nic",
      image: new Uint8Array([1, 2, 3])
    });
    // Bytes a caller already holds go as they are.
    const held = new Uint8Array([9]);
    expect(encodeMessage(SetAccountAvatarRequestSchema, { image: held }).image).toBe(held);
  });

  it("drops undefined, null and fields the message does not have", () => {
    expect(encodeMessage(ListChangesetsRequestSchema, { status: undefined, query: null, nonsense: 1, summary: true })).toEqual({
      summary: true
    });
  });

  it("converts nested messages and keeps a oneof as {case, value}", () => {
    expect(
      encodeMessage(ListChangesetsRequestSchema, { authoringSlice: { account: "acme", slice: "shop" }, statuses: ["draft"] })
    ).toEqual({ authoringSlice: { account: "acme", slice: "shop" }, statuses: ["draft"] });
    expect(encodeMessage(UploadBlobChunkSchema, { payload: { case: "data", value: "AQ==" } })).toEqual({
      payload: { case: "data", value: new Uint8Array([1]) }
    });
  });
});

describe("decodeMessage", () => {
  it("turns int64 into decimal strings, bytes into base64 and enums into names", () => {
    const response = create(ListDirectoryResponseSchema, {
      entries: [
        { name: "a.go", kind: EntryKind.FILE, size: 9007199254740993n },
        { name: "src", kind: EntryKind.DIRECTORY }
      ],
      nextCursor: "c"
    });
    expect(decodeMessage(ListDirectoryResponseSchema, response)).toMatchObject({
      entries: [
        { name: "a.go", kind: "ENTRY_KIND_FILE", size: "9007199254740993" },
        { name: "src", kind: "ENTRY_KIND_DIRECTORY" }
      ],
      nextCursor: "c"
    });
  });

  it("encodes bytes as base64 and leaves out the message bookkeeping", () => {
    const message = create(SetAccountAvatarRequestSchema, { account: "nic", image: new Uint8Array([1, 2, 3]) });
    const decoded = decodeMessage<Record<string, unknown>>(SetAccountAvatarRequestSchema, message);
    expect(decoded).toEqual({ account: "nic", image: "AQID" });
    expect(Object.keys(decoded)).not.toContain("$typeName");
  });

  it("names an enum number it does not know as the number", () => {
    const decoded = decodeMessage(ListDirectoryResponseSchema, { entries: [{ kind: 99 }] });
    expect(decoded).toMatchObject({ entries: [{ kind: 99 }] });
  });

  it("keeps a oneof as {case, value} and an unset one as {case: undefined}", () => {
    const set = create(UploadBlobChunkSchema, { payload: { case: "data", value: new Uint8Array([1]) } });
    expect(decodeMessage(UploadBlobChunkSchema, set)).toEqual({ payload: { case: "data", value: "AQ==" } });
    expect(decodeMessage(UploadBlobChunkSchema, create(UploadBlobChunkSchema))).toEqual({ payload: { case: undefined } });
  });
});
