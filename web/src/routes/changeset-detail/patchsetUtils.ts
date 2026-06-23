import type { Changeset, Patchset } from "../../api/types";
import { shortHash } from "../../lib/objectId";

export type TimelineHandle = "from" | "to";

export interface TimelineStep {
  id: string;
  label: string;
  patchset?: Patchset;
}

export function sortedPatchsets(changeset?: Changeset) {
  return [...(changeset?.patchsets || [])].sort((left, right) => {
    const leftNumber = numericPatchsetNumber(left);
    const rightNumber = numericPatchsetNumber(right);
    if (leftNumber !== rightNumber) {
      return leftNumber - rightNumber;
    }

    return patchsetKey(left).localeCompare(patchsetKey(right));
  });
}

export function numericPatchsetNumber(patchset: Patchset) {
  const number = Number(patchset.number);
  return Number.isFinite(number) ? number : Number.MAX_SAFE_INTEGER;
}

export function patchsetKey(patchset: Patchset) {
  return (
    patchset.id ||
    `${patchset.number || "unknown"}-${patchset.createdAt || ""}-${
      patchset.baseCommitId || patchset.basePatchsetId || ""
    }`
  );
}

export function findPatchset(patchsets: Patchset[], patchsetId: string) {
  return patchsets.find((patchset) => patchset.id === patchsetId);
}

export function patchsetOptionLabel(patchset?: Patchset) {
  if (!patchset) {
    return "";
  }
  if (patchset.number !== undefined && patchset.number !== "") {
    return `Patchset ${patchset.number}`;
  }

  const shortId = shortPatchsetId(patchset.id || "");
  return shortId ? `Patchset ${shortId}` : "Patchset";
}

export function patchsetDotLabel(patchset: Patchset) {
  if (patchset.number !== undefined && patchset.number !== "") {
    return `P${patchset.number}`;
  }

  const shortId = shortPatchsetId(patchset.id || "");
  return shortId ? `P${shortId}` : "P";
}

export function shortCommit(commitId: string) {
  return shortHash(commitId);
}

export function shortPatchsetId(patchsetId: string) {
  if (!patchsetId) {
    return "";
  }
  return patchsetId.replace(/^ps_/, "").slice(0, 12);
}

export function patchsetConversationRange(
  patchsets: Patchset[],
  selectedPatchsetId: string,
  fromPatchsetId?: string
): { conversationId: string; afterSeq: number; beforeSeq: number } | null {
  const selected = patchsets.find((patchset) => patchset.id === selectedPatchsetId);
  const conversationId = selected?.authoringConversationId ?? "";
  if (!selected || !conversationId) {
    return null;
  }
  const beforeSeq = Number(selected.authoringConversationSeq ?? 0);

  // When the comparison's "from" handle is provided, trim the conversation to
  // the segment that produced the visible diff range (from, to] instead of just
  // the patchset that immediately precedes the selected one.
  if (fromPatchsetId !== undefined) {
    // An empty "from" means the recorded base: include the whole conversation
    // up to the selected patchset, mirroring the full-changeset diff.
    if (!fromPatchsetId) {
      return { conversationId, afterSeq: 0, beforeSeq };
    }
    const from = patchsets.find((patchset) => patchset.id === fromPatchsetId);
    const afterSeq =
      from && from.authoringConversationId === conversationId
        ? Number(from.authoringConversationSeq ?? 0)
        : 0;
    return { conversationId, afterSeq, beforeSeq };
  }

  const selectedNumber = Number(selected.number ?? 0);
  let afterSeq = 0;
  for (const patchset of patchsets) {
    if (patchset.authoringConversationId !== conversationId) {
      continue;
    }
    const number = Number(patchset.number ?? 0);
    if (number < selectedNumber) {
      afterSeq = Math.max(afterSeq, Number(patchset.authoringConversationSeq ?? 0));
    }
  }
  return { conversationId, afterSeq, beforeSeq };
}

export function timelineIndexForValue(
  steps: TimelineStep[],
  value: string,
  fallback: number
) {
  if (!value) {
    return 0;
  }

  const index = steps.findIndex((step) => step.id === value);
  return index >= 0 ? index : fallback;
}

export function timelinePosition(index: number, maxIndex: number) {
  if (maxIndex <= 0) {
    return "0%";
  }
  return `${(index / maxIndex) * 100}%`;
}

export function handleTransform(index: number, maxIndex: number) {
  if (index <= 0) {
    return "translateX(0)";
  }
  if (maxIndex > 0 && index >= maxIndex) {
    return "translateX(-100%)";
  }
  return "translateX(-50%)";
}

export function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value));
}