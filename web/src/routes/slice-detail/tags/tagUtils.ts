import type { SliceRef, Tag } from "../../../api/types";
import { shortHash } from "../../../lib/objectId";

// Release tags of Gitslice itself; their files are served under /releases.
export const RELEASE_TAG = /^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;
export const FILTER_INPUT_THRESHOLD = 5;

// releaseHrefForTag is the release page of a tag, for the gs CLI releases
// that gitslice/gitslice tags start. Other slices have no release files.
export function releaseHrefForTag(
  sliceRef: SliceRef | undefined,
  name: string,
): string | undefined {
  if (
    sliceRef?.account !== "gitslice" ||
    sliceRef?.slice !== "gitslice" ||
    !RELEASE_TAG.test(name)
  ) {
    return undefined;
  }
  return `/releases/tag/${name}`;
}

export function shortCommitHash(commitId: string | undefined): string {
  return shortHash(commitId, 7);
}

export function originSliceForTag(
  tag: Tag,
  sliceRef: SliceRef | undefined,
): { account: string; slice: string; label: string } | undefined {
  const account = tag.slice?.account;
  const slice = tag.slice?.slice;
  if (!account || !slice) {
    return undefined;
  }
  const label = account === sliceRef?.account ? slice : `${account}/${slice}`;
  return { account, slice, label };
}

// tagSummary drops a leading "v1.2.3:" (or "gs v1.2.3:") from a tag's message,
// since the row already shows the name.
export function tagSummary(name: string, message: string): string {
  const text = message.trim();
  for (const prefix of [`${name}:`, `gs ${name}:`]) {
    if (name && text.startsWith(prefix)) {
      return text.slice(prefix.length).trim();
    }
  }
  return text;
}
