import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useRouter } from "@tanstack/react-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { AnalyticsEvents, AnalyticsProps } from "../../analytics/events";
import { capture } from "../../analytics/posthog";
import type { ApiClient } from "../../api/useApi";
import { sliceDisplayName } from "../../components/slices/SlicePageParts";
import {
  decodeBase64File,
  isSliceProjectionDirectoryPath,
  syntheticDirectoryEntry
} from "../../components/source/sourceUtils";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
import {
  globalRefQuery,
  sliceDirectoryQuery,
  sliceFileQuery,
  slicePathQuery,
  sliceRefQuery
} from "../../queries/slices";
import type { LandedPathOperation } from "./CopyMove";
import { useFolderReadme } from "./nestedReadme";

export interface UseSlicePathExplorerOptions {
  api: ApiClient;
  isLoaded: boolean;
  routeAccount: string;
  routeSlice: string;
  selectedPath: string;
  historyOpen: boolean;
  policyOpen: boolean;
}

export function useSlicePathExplorer({
  api,
  isLoaded,
  routeAccount,
  routeSlice,
  selectedPath,
  historyOpen,
  policyOpen
}: UseSlicePathExplorerOptions) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const router = useRouter();

  const routeSliceRef = useMemo(
    () =>
      routeAccount && routeSlice
        ? { account: routeAccount, slice: routeSlice }
        : undefined,
    [routeAccount, routeSlice]
  );

  const [treeChoice, setTreeChoice] = useState<boolean | null>(null);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [finderOpen, setFinderOpen] = useState(false);

  useEffect(() => {
    setDetailsOpen(false);
  }, [selectedPath]);

  const sliceQuery = useQuery({
    ...sliceRefQuery(api, routeAccount, routeSlice),
    enabled: Boolean(isLoaded && routeSliceRef)
  });

  const latestQuery = useQuery({
    ...globalRefQuery(api),
    enabled: Boolean(isLoaded && routeSliceRef && !sliceQuery.isError)
  });

  const slice = sliceQuery.data;
  const sliceRef = slice?.ref ?? routeSliceRef;
  const sliceId = slice?.id ?? "";
  const sliceRouteParams = toSliceRouteParams(sliceRef);
  const sliceRouteKey = sliceRouteParams
    ? `${sliceRouteParams.account}:${sliceRouteParams.slice}`
    : `${routeAccount}:${routeSlice}`;
  const sliceLabel = sliceDisplayName(slice);
  const commitId = latestQuery.data?.commitId ?? "";
  const canFindFiles = Boolean(commitId && slice);

  // 't' opens the file finder when focus isn't inside an editable field.
  useEffect(() => {
    if (!canFindFiles) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (
        event.key !== "t" ||
        event.defaultPrevented ||
        event.metaKey ||
        event.ctrlKey ||
        event.altKey ||
        event.shiftKey
      ) {
        return;
      }
      const target = event.target as HTMLElement | null;
      if (
        target &&
        (target.isContentEditable || /^(?:input|textarea|select)$/i.test(target.tagName))
      ) {
        return;
      }
      event.preventDefault();
      setFinderOpen(true);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [canFindFiles]);

  // Redirect old slice name to canonical slice name after a rename.
  const currentSliceName = slice?.ref?.slice ?? "";
  useEffect(() => {
    if (
      !currentSliceName ||
      !routeSlice ||
      currentSliceName === routeSlice ||
      !sliceRouteParams
    ) {
      return;
    }
    void navigate({
      params: sliceRouteParams,
      replace: true,
      search: selectedPath ? { path: selectedPath } : {},
      to: "/slices/$account/$slice"
    });
  }, [currentSliceName, navigate, routeSlice, selectedPath, sliceRouteParams]);

  useEffect(() => {
    if (!sliceId) {
      return;
    }
    capture(AnalyticsEvents.sliceViewed, {
      [AnalyticsProps.sliceId]: sliceId
    });
  }, [sliceId]);

  const includedPaths = slice?.definition?.includedPaths ?? [];
  const isProjectedDirectoryPath = isSliceProjectionDirectoryPath(
    selectedPath,
    includedPaths
  );

  const pathQuery = useQuery({
    enabled: Boolean(
      commitId &&
        selectedPath &&
        !isProjectedDirectoryPath &&
        sliceRef?.account &&
        sliceRef?.slice
    ),
    ...slicePathQuery(api, sliceRef, commitId, selectedPath)
  });

  const entry = isProjectedDirectoryPath
    ? syntheticDirectoryEntry(selectedPath)
    : pathQuery.data?.entry;
  const isDirectory = entry?.kind === "ENTRY_KIND_DIRECTORY";
  const isFile = entry?.kind === "ENTRY_KIND_FILE";

  const directoryQuery = useQuery({
    enabled: Boolean(
      commitId && sliceRef?.account && sliceRef?.slice && isDirectory
    ),
    ...sliceDirectoryQuery(api, sliceRef, commitId, selectedPath, {
      allowMissingDirectory: isProjectedDirectoryPath
    })
  });

  const folderEntries =
    isDirectory && !directoryQuery.isPending && !directoryQuery.error
      ? (directoryQuery.data ?? [])
      : [];
  const { loading: readmeLoading, readme: folderReadme } = useFolderReadme(
    commitId,
    sliceRef,
    folderEntries
  );

  const folderKnown =
    entry !== undefined &&
    (!isDirectory || (!directoryQuery.isPending && !readmeLoading));
  const readmeShown = isDirectory && Boolean(folderReadme);
  const lastReadmeShown = useRef<boolean | null>(null);
  if (folderKnown) {
    lastReadmeShown.current = readmeShown;
  }
  const foldTree = folderKnown ? readmeShown : (lastReadmeShown.current ?? true);
  const readmeExpected = folderKnown
    ? readmeShown
    : (lastReadmeShown.current ?? false);
  const showTree = treeChoice ?? !foldTree;

  const fileQuery = useQuery({
    enabled: Boolean(
      commitId &&
        selectedPath &&
        !isDirectory &&
        !isProjectedDirectoryPath &&
        sliceRef?.account &&
        sliceRef?.slice
    ),
    ...sliceFileQuery(api, sliceRef, commitId, selectedPath)
  });

  const selectPath = useCallback(
    (path: string) => {
      if (!sliceRouteParams) {
        return;
      }
      void navigate({
        params: sliceRouteParams,
        search: path ? { path } : {},
        to: "/slices/$account/$slice"
      });
    },
    [navigate, sliceRouteParams]
  );

  const setBlame = useCallback(
    (on: boolean, at: string) => {
      if (!sliceRouteParams) {
        return;
      }
      void navigate({
        params: sliceRouteParams,
        replace: true,
        resetScroll: false,
        search: {
          ...(selectedPath ? { path: selectedPath } : {}),
          ...(on ? { blame: true as const } : {}),
          ...(on && at ? { blameAt: at } : {})
        },
        to: "/slices/$account/$slice"
      });
    },
    [navigate, selectedPath, sliceRouteParams]
  );

  const openHistory = useCallback(() => {
    if (!sliceRouteParams) {
      return;
    }
    void navigate({
      params: sliceRouteParams,
      search: {
        ...(selectedPath ? { path: selectedPath } : {}),
        history: true
      },
      to: "/slices/$account/$slice"
    });
  }, [navigate, selectedPath, sliceRouteParams]);

  const openPolicy = useCallback(() => {
    if (!sliceRouteParams) {
      return;
    }
    void navigate({
      params: sliceRouteParams,
      search: {
        ...(selectedPath ? { path: selectedPath } : {}),
        policy: true
      },
      to: "/slices/$account/$slice"
    });
  }, [navigate, selectedPath, sliceRouteParams]);

  const closePolicy = useCallback(() => {
    if (policyOpen) {
      router.history.back();
    }
  }, [policyOpen, router.history]);

  const closeHistory = useCallback(() => {
    if (historyOpen) {
      router.history.back();
    }
  }, [historyOpen, router.history]);

  const openLandedPath = useCallback(
    async (landed: LandedPathOperation) => {
      await queryClient.invalidateQueries({ queryKey: ["globalRef"] });
      const params = toSliceRouteParams(landed.slice);
      if (!params) {
        return;
      }
      await navigate({
        params,
        search: { path: landed.path },
        to: "/slices/$account/$slice"
      });
    },
    [navigate, queryClient]
  );

  const phoneTreeBar = readmeExpected || (!selectedPath && treeChoice !== null);
  const treePanelVisibility = phoneTreeBar
    ? showTree
      ? "block"
      : "hidden"
    : selectedPath
      ? showTree
        ? "hidden lg:block"
        : "hidden"
      : showTree
        ? "block"
        : "hidden";
  const workspaceVisibility = phoneTreeBar
    ? "block max-lg:order-3"
    : selectedPath
      ? "block"
      : "block max-lg:order-first";

  const projectedRoots = includedPaths
    .map((includedPath) => includedPath.replace(/^\/+|\/+$/g, ""))
    .filter(Boolean);
  const createDirectory =
    selectedPath || (projectedRoots.length === 1 ? projectedRoots[0] : "");

  return {
    sliceQuery,
    latestQuery,
    pathQuery,
    directoryQuery,
    fileQuery,
    slice,
    sliceRef,
    routeSliceRef,
    sliceId,
    sliceRouteParams,
    sliceRouteKey,
    sliceLabel,
    commitId,
    includedPaths,
    createDirectory,
    entry,
    isDirectory,
    isFile,
    currentEntries: directoryQuery.data ?? [],
    fileContent: decodeBase64File(fileQuery.data?.data),
    fileData: fileQuery.data?.data ?? "",
    folderReadme,
    readmeLoading,
    showTree,
    setTreeChoice,
    phoneTreeBar,
    treePanelVisibility,
    workspaceVisibility,
    detailsOpen,
    setDetailsOpen,
    finderOpen,
    setFinderOpen,
    selectPath,
    setBlame,
    openHistory,
    closeHistory,
    openPolicy,
    closePolicy,
    openLandedPath
  };
}
