import { useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";

import { useApi } from "../../api/useApi";
import type { SliceRef } from "../../api/types";
import { sliceFileQueryKey } from "../../lib/sliceQueryKeys";
import { imageMimeTypeFromPath } from "./ImageViewer";

/**
 * useRepoImageLoader returns a function that loads an image file of the
 * repository, at the commit being viewed, as a data: URL a markdown <img> can
 * show. It reads through the API, so it works for private slices too, and
 * resolves to null for a path that is not an image or cannot be read (missing,
 * too large); the viewer then shows the image's alt text.
 */
export function useRepoImageLoader(commitId: string, slice: SliceRef | undefined) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useCallback(
    async (path: string): Promise<string | null> => {
      const mimeType = imageMimeTypeFromPath(path);
      if (!mimeType || !commitId || !slice?.account || !slice?.slice) {
        return null;
      }
      const file = await queryClient.fetchQuery({
        // The file view reads the same key, so opening the image is instant.
        queryKey: sliceFileQueryKey(slice, commitId, path),
        queryFn: () => api.readFile({ commitId, path, slice }),
        staleTime: Infinity
      });
      return file.data ? `data:${mimeType};base64,${file.data}` : null;
    },
    [api, queryClient, commitId, slice]
  );
}
