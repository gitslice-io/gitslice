import { useCallback, useEffect, useRef, useState } from "react";

import { loadViewed, saveViewed, type ViewedFiles } from "./reviewState";

// The files the reviewer has marked viewed in one changeset (the scope), kept
// on this browser. The server renders the page without them, so they are read
// once the page is in the browser; without a scope they last as long as the
// page does.
export function useViewedFiles(scope: string | undefined) {
  const [viewed, setViewed] = useState<ViewedFiles>({});
  const current = useRef<ViewedFiles>({});

  useEffect(() => {
    const loaded = scope ? loadViewed(scope) : {};
    current.current = loaded;
    setViewed(loaded);
  }, [scope]);

  // fingerprint marks the file viewed as it is now; null clears the mark.
  const mark = useCallback(
    (path: string, fingerprint: string | null) => {
      const next = { ...current.current };
      if (fingerprint === null) {
        delete next[path];
      } else {
        next[path] = fingerprint;
      }
      current.current = next;
      setViewed(next);
      if (scope) {
        saveViewed(scope, next);
      }
    },
    [scope]
  );

  return { viewed, mark };
}
