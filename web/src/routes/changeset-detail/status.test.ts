import { describe, expect, it } from "vitest";

import { RpcError, isTransientTransportError } from "../../api/rpcError";
import { mergeErrorMessage } from "./status";

describe("mergeErrorMessage", () => {
  it("reports transient RpcError codes with the incomplete-merge guidance", () => {
    for (const error of [
      new RpcError(504, { code: "deadline_exceeded", message: "deadline exceeded" }),
      new RpcError(503, { code: "unavailable", message: "unavailable" }),
      new RpcError(499, { code: "canceled", message: "canceled" }),
      new RpcError(504, { code: 4, message: "timeout" }),
      new TypeError("Failed to fetch")
    ]) {
      expect(mergeErrorMessage(error)).toContain("The merge request did not complete.");
    }
  });

  it("returns the server error message for non-transient RpcErrors", () => {
    const err = new RpcError(412, { code: "failed_precondition", message: "needs approval" });
    expect(isTransientTransportError(err)).toBe(false);
    expect(mergeErrorMessage(err)).toBe("needs approval");
  });
});
