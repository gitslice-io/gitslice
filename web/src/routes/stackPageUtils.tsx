import type { SliceRef } from "../api/types";
import { parseSliceRef } from "../lib/sliceRoutes";

export function parseSliceSearch(value: unknown): Required<SliceRef> | null {
  return parseSliceRef(value);
}

export function displaySubmitBlockedReason(reason?: string) {
  switch ((reason || "").trim()) {
    case "NeedsRestack":
    case "NeedsBaseUpdate":
      return "needs base update";
    case "BlockedOnStackParent":
    case "BlockedOnBaseChangeset":
      return "base changeset has not landed";
    default:
      return reason || "";
  }
}
