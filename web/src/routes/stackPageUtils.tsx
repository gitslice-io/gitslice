import type { SliceRef } from "../api/types";

export function parseSliceSearch(value: unknown): Required<SliceRef> | null {
  if (typeof value !== "string") {
    return null;
  }

  const trimmed = value.trim();
  const sep = trimmed.includes(":") ? ":" : "/";
  const index = trimmed.indexOf(sep);

  if (index <= 0) {
    return null;
  }

  const account = trimmed.slice(0, index).trim();
  const slice = trimmed.slice(index + 1).trim();

  if (!account || !slice) {
    return null;
  }

  return { account, slice };
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
