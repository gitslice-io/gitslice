import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { RevealState } from "./collapse";
import { EMPTY_REVEAL, EXPAND_CHUNK, type ExpandFn } from "./diffViewShared";
import type { DiffFile } from "./parseDiff";
import { fileFingerprint, isGeneratedFile, isViewed } from "./reviewState";
import { useViewedFiles } from "./useViewedFiles";

export const LAZY_DIFF_BODY_ROOT_MARGIN = "1200px 0px 1200px 0px";
export const SCROLL_SPY_ROOT_MARGIN = "-112px 0px -65% 0px";

export interface UseDiffViewportControllerOptions {
  files: DiffFile[];
  diffText?: string;
  focusFilePath?: string;
  viewedScope?: string;
  onFileNeeded?: (path: string) => void;
}

export function useDiffViewportController({
  files,
  diffText,
  focusFilePath,
  viewedScope,
  onFileNeeded
}: UseDiffViewportControllerOptions) {
  const fileListKey = files.map((file) => file.id).join("\0");
  const fileIds = useMemo(
    () => new Set(files.map((file) => file.id)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [fileListKey]
  );
  const filePathsById = useMemo(
    () => new Map(files.map((file) => [file.id, file.path])),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [fileListKey]
  );

  const [activeId, setActiveId] = useState<string | undefined>();
  const [mountedDiffBodies, setMountedDiffBodies] = useState<Set<string>>(
    () => new Set()
  );
  const { viewed, mark } = useViewedFiles(viewedScope);
  const [collapseOverride, setCollapseOverride] = useState<Record<string, boolean>>({});
  const [revealed, setRevealed] = useState<Record<string, RevealState>>({});
  const panelRefs = useRef<Record<string, HTMLElement | null>>({});
  const pinnedActiveIdRef = useRef<string | null>(null);
  const focusAppliedRef = useRef<string | null>(null);
  const [focusTargetId, setFocusTargetId] = useState<string | null>(null);

  const activeFileIndex = files.findIndex((file) => file.id === activeId);
  const currentActiveId = files[activeFileIndex >= 0 ? activeFileIndex : 0]?.id;
  const firstFileId = files[0]?.id;

  // Reset collapse state whenever a new diff loads.
  useEffect(() => {
    setRevealed({});
    setCollapseOverride({});
  }, [diffText, fileListKey]);

  const expandGap = useCallback<ExpandFn>((key, action, hiddenTotal) => {
    setRevealed((current) => {
      const reveal = current[key] ?? EMPTY_REVEAL;
      let next: RevealState;
      if (action === "all") {
        next = { top: hiddenTotal, bottom: 0 };
      } else if (action === "up") {
        next = {
          top: Math.min(hiddenTotal - reveal.bottom, reveal.top + EXPAND_CHUNK),
          bottom: reveal.bottom
        };
      } else {
        next = {
          top: reveal.top,
          bottom: Math.min(hiddenTotal - reveal.top, reveal.bottom + EXPAND_CHUNK)
        };
      }
      return { ...current, [key]: next };
    });
  }, []);

  useEffect(() => {
    if (pinnedActiveIdRef.current && !fileIds.has(pinnedActiveIdRef.current)) {
      pinnedActiveIdRef.current = null;
    }
    if (!fileIds.size) {
      setActiveId(undefined);
      return;
    }
    setActiveId((current) =>
      current && fileIds.has(current) ? current : firstFileId
    );
  }, [fileIds, firstFileId]);

  // Release the active-file pin as soon as the user scrolls themselves.
  useEffect(() => {
    const release = () => {
      pinnedActiveIdRef.current = null;
    };
    window.addEventListener("wheel", release, { passive: true });
    window.addEventListener("touchmove", release, { passive: true });
    window.addEventListener("keydown", release);
    return () => {
      window.removeEventListener("wheel", release);
      window.removeEventListener("touchmove", release);
      window.removeEventListener("keydown", release);
    };
  }, []);

  useEffect(() => {
    setMountedDiffBodies((current) => {
      const next = new Set<string>();
      current.forEach((id) => {
        if (fileIds.has(id)) {
          next.add(id);
        }
      });
      return next.size === current.size ? current : next;
    });
  }, [fileIds]);

  // Active-file scroll-spy observer.
  useEffect(() => {
    if (!files.length || typeof IntersectionObserver === "undefined") {
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        const visibleEntries = entries
          .filter((entry) => entry.isIntersecting)
          .sort(
            (left, right) =>
              left.boundingClientRect.top - right.boundingClientRect.top
          );

        if (pinnedActiveIdRef.current) {
          return;
        }

        const topEntry = visibleEntries[0];
        if (topEntry?.target.id) {
          setActiveId(topEntry.target.id);
        }
      },
      {
        root: null,
        rootMargin: SCROLL_SPY_ROOT_MARGIN,
        threshold: [0, 0.05, 0.2]
      }
    );

    fileIds.forEach((id) => {
      const panel = panelRefs.current[id];
      if (panel) {
        observer.observe(panel);
      }
    });

    return () => observer.disconnect();
  }, [fileIds, files.length]);

  // Lazy diff body mounting & per-file demand-loading observer.
  useEffect(() => {
    if (!files.length) {
      return;
    }

    if (typeof IntersectionObserver === "undefined") {
      setMountedDiffBodies(new Set(fileIds));
      filePathsById.forEach((path) => onFileNeeded?.(path));
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        const visibleIds = entries
          .filter((entry) => entry.isIntersecting && entry.target.id)
          .map((entry) => entry.target.id);

        if (!visibleIds.length) {
          return;
        }

        visibleIds.forEach((id) => {
          const path = filePathsById.get(id);
          if (path) {
            onFileNeeded?.(path);
          }
        });

        setMountedDiffBodies((current) => {
          const next = new Set(current);
          let changed = false;
          visibleIds.forEach((id) => {
            if (!next.has(id)) {
              next.add(id);
              changed = true;
            }
          });
          return changed ? next : current;
        });
      },
      {
        root: null,
        rootMargin: LAZY_DIFF_BODY_ROOT_MARGIN,
        threshold: 0
      }
    );

    fileIds.forEach((id) => {
      const panel = panelRefs.current[id];
      if (panel) {
        observer.observe(panel);
      }
    });

    return () => observer.disconnect();
  }, [fileIds, filePathsById, files.length, onFileNeeded]);

  const mountFileBody = useCallback((id: string) => {
    setMountedDiffBodies((current) => {
      if (current.has(id)) {
        return current;
      }
      const next = new Set(current);
      next.add(id);
      return next;
    });
  }, []);

  const selectFile = useCallback(
    (id: string, behavior: ScrollBehavior = "smooth") => {
      pinnedActiveIdRef.current = id;
      setActiveId(id);
      mountFileBody(id);
      const path = filePathsById.get(id);
      if (path) {
        onFileNeeded?.(path);
      }
      window.setTimeout(() => {
        document.getElementById(id)?.scrollIntoView({
          behavior,
          block: "start"
        });
      }, 0);
    },
    [filePathsById, mountFileBody, onFileNeeded]
  );

  // Keep deep-linked target file in view while surrounding diffs load.
  useEffect(() => {
    if (!focusTargetId || typeof ResizeObserver === "undefined") {
      return;
    }
    const keepInView = () => {
      if (pinnedActiveIdRef.current === focusTargetId) {
        document.getElementById(focusTargetId)?.scrollIntoView({ block: "start" });
      }
    };
    const observer = new ResizeObserver(keepInView);
    observer.observe(document.body);
    const stop = window.setTimeout(() => observer.disconnect(), 20_000);
    return () => {
      observer.disconnect();
      window.clearTimeout(stop);
    };
  }, [focusTargetId]);

  // Honor `?file=` deep link once per requested path.
  useEffect(() => {
    if (!focusFilePath || !files.length) {
      return;
    }
    const wanted = focusFilePath.replace(/^\/+/, "");
    const target = files.find((file) => file.path.replace(/^\/+/, "") === wanted);
    if (!target || focusAppliedRef.current === focusFilePath) {
      return;
    }
    focusAppliedRef.current = focusFilePath;
    selectFile(target.id, "auto");
    setFocusTargetId(target.id);
  }, [files, focusFilePath, selectFile]);

  const viewedIds = useMemo(
    () => new Set(files.filter((file) => isViewed(file, viewed)).map((file) => file.id)),
    [files, viewed]
  );

  const isCollapsed = useCallback(
    (file: DiffFile) =>
      collapseOverride[file.id] ?? (viewedIds.has(file.id) || isGeneratedFile(file)),
    [collapseOverride, viewedIds]
  );

  const toggleCollapsed = useCallback(
    (file: DiffFile) => {
      const collapse = !isCollapsed(file);
      setCollapseOverride((current) => ({ ...current, [file.id]: collapse }));
      if (!collapse) {
        mountFileBody(file.id);
        onFileNeeded?.(file.path);
      }
    },
    [isCollapsed, mountFileBody, onFileNeeded]
  );

  const toggleViewed = useCallback(
    (file: DiffFile) => {
      if (file.lines.length === 0) {
        return;
      }
      const wasViewed = viewedIds.has(file.id);
      mark(file.path, wasViewed ? null : fileFingerprint(file));
      setCollapseOverride((current) => ({ ...current, [file.id]: !wasViewed }));
      if (wasViewed) {
        mountFileBody(file.id);
      }
    },
    [mark, mountFileBody, viewedIds]
  );

  // j and k move between files, v marks the one in view as viewed.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) {
        return;
      }
      const target = event.target as HTMLElement | null;
      if (target && (target.isContentEditable || /^(?:input|textarea|select)$/i.test(target.tagName))) {
        return;
      }
      if (event.key !== "j" && event.key !== "k" && event.key !== "v") {
        return;
      }
      const index = Math.max(0, files.findIndex((file) => file.id === currentActiveId));
      if (event.key === "v") {
        if (files[index]) {
          toggleViewed(files[index]);
        }
        return;
      }
      const next = files[index + (event.key === "j" ? 1 : -1)];
      if (next) {
        selectFile(next.id);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [files, currentActiveId, toggleViewed, selectFile]);

  const setPanelRef = useCallback((id: string, node: HTMLElement | null) => {
    panelRefs.current[id] = node;
  }, []);

  return {
    activeId,
    currentActiveId,
    mountedDiffBodies,
    viewedIds,
    revealed,
    expandGap,
    isCollapsed,
    toggleCollapsed,
    toggleViewed,
    selectFile,
    setPanelRef
  };
}
