import { useCallback, useSyncExternalStore } from "react";

function getServerSnapshot(): boolean {
  return false;
}

/**
 * SSR- and jsdom-safe media query hook backed by `useSyncExternalStore`.
 * Always returns `false` on the server / hydration pass and when `matchMedia`
 * is unavailable, then tracks live `change` events on the client.
 */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onStoreChange: () => void) => {
      if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
        return () => {};
      }
      const mediaQueryList = window.matchMedia(query);
      mediaQueryList.addEventListener("change", onStoreChange);
      return () => {
        mediaQueryList.removeEventListener("change", onStoreChange);
      };
    },
    [query]
  );

  const getSnapshot = useCallback(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return false;
    }
    return window.matchMedia(query).matches;
  }, [query]);

  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}
