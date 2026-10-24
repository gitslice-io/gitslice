import type { ApiClient } from "../../api/useApi";
import type { ListChangesetsRequest, ListChangesetsResponse, SliceRef } from "../../api/types";

// The changesets list shows one status group at a time (the review queue is
// the open ones) and can narrow it by text and author. They live in the URL,
// so a filtered list can be shared and the back button steps through it.

export type StatusTab = "open" | "landed" | "abandoned" | "all";

export const statusTabs: { value: StatusTab; label: string; statuses: string[] }[] = [
  // A changeset starts as a draft; "published" and "open" are older names for
  // work in progress, and pending_publish is the moment of landing.
  { value: "open", label: "Open", statuses: ["draft", "open", "published", "pending_publish"] },
  { value: "landed", label: "Landed", statuses: ["submitted", "merged"] },
  { value: "abandoned", label: "Abandoned", statuses: ["abandoned"] },
  { value: "all", label: "All", statuses: [] }
];

export const CHANGESETS_PAGE_SIZE = 50;

// Open is the default and keeps the address clean.
export function parseStatusTab(value: unknown): StatusTab {
  return statusTabs.find((tab) => tab.value === value)?.value ?? "open";
}

export interface ChangesetSearch {
  // Text to find in a title, or the start of a changeset's id.
  query: string;
  // A username from `author:name`; "" for any author.
  author: string;
}

// parseSearch splits what was typed into the text and the author: any
// `author:name` word names the author (the last one counts), the rest is text.
export function parseSearch(typed: string): ChangesetSearch {
  let author = "";
  const words: string[] = [];
  for (const word of typed.trim().split(/\s+/)) {
    const match = /^author:(.*)$/i.exec(word);
    if (match) {
      author = match[1].replace(/^@/, "").toLowerCase();
    } else if (word) {
      words.push(word);
    }
  }
  return { query: words.join(" "), author };
}

export function changesetsListKeyPrefix(slice: SliceRef) {
  return ["changesets", slice.account ?? "", slice.slice ?? ""] as const;
}

export function changesetsListRequest(
  slice: SliceRef,
  tab: StatusTab,
  typed: string,
  pageToken: string
): ListChangesetsRequest {
  const { query, author } = parseSearch(typed);
  const statuses = statusTabs.find((entry) => entry.value === tab)?.statuses ?? [];
  return {
    authoringSlice: slice,
    // The list shows each changeset's own fields; patchsets are the heavy
    // part of the answer.
    summary: true,
    limit: CHANGESETS_PAGE_SIZE,
    ...(statuses.length > 0 ? { statuses } : {}),
    ...(query ? { query } : {}),
    ...(author ? { author } : {}),
    ...(pageToken ? { pageToken } : {})
  };
}

// The options of the list's infinite query, shared by the page and the server
// render that prefetches its first page.
export function changesetsListQuery(api: ApiClient, slice: SliceRef, tab: StatusTab, typed: string) {
  return {
    queryKey: [...changesetsListKeyPrefix(slice), tab, typed.trim()] as const,
    initialPageParam: "",
    queryFn: ({ pageParam }: { pageParam: string }): Promise<ListChangesetsResponse> =>
      api.listChangesets(changesetsListRequest(slice, tab, typed, pageParam)),
    getNextPageParam: (last: ListChangesetsResponse) => last.nextPageToken || undefined
  };
}
