import type { SliceRef } from "../../api/types";
import type { ApiClient } from "../../api/useApi";

export interface DependentChangeset {
  id: string;
  title: string;
}

// dependentChangesetsQuery finds the changesets stacked directly on one: the
// slice's recent changesets whose parent it is. Only their ids and titles are
// kept. The list it is read from is 200 changesets with their paths and
// descriptions (about 400 KB); cached whole, the server render put all of it
// into every changeset page's HTML to show what is usually no rows at all.
export function dependentChangesetsQuery(
  api: Pick<ApiClient, "listChangesets">,
  authoringSlice: SliceRef | undefined,
  changesetId: string
) {
  return {
    queryKey: ["changesetDependents", authoringSlice?.account ?? "", authoringSlice?.slice ?? "", changesetId] as const,
    queryFn: async (): Promise<DependentChangeset[]> => {
      const listed = await api.listChangesets({ authoringSlice, limit: 200, summary: true });
      return (listed.changesets ?? [])
        .filter((candidate) => candidate.parentChangesetId === changesetId && candidate.id)
        .map((candidate) => ({ id: candidate.id as string, title: candidate.title ?? "" }));
    }
  };
}
