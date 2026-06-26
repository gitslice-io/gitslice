import { useRouter } from "@tanstack/react-router";
import { useCallback, type MouseEvent } from "react";

// Root-relative app paths (e.g. /cs/..., /slices/...) the server emits when it
// resolves agent gsfile: links. Protocol-relative ("//host") and scheme URLs
// (http:, mailto:, …) are external and left to the browser.
function isInternalHref(href: string): boolean {
  return href.startsWith("/") && !href.startsWith("//");
}

// useInternalLinkClickHandler returns an onClick handler for a container whose
// inner HTML is sanitized markdown (rendered via dangerouslySetInnerHTML, so its
// links are plain <a> elements, not router <Link>s). An unmodified left-click on
// a same-origin app link is turned into a client-side route change, so it swaps
// the view without a full page reload that would reboot auth/session and refetch
// everything. External links, modified clicks, and explicit new-tab targets
// (open-in-new-tab intent) are left to the browser untouched.
export function useInternalLinkClickHandler() {
  const router = useRouter();
  return useCallback(
    (event: MouseEvent<HTMLElement>) => {
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey
      ) {
        return;
      }
      const anchor = (event.target as HTMLElement | null)?.closest("a");
      if (!anchor) {
        return;
      }
      const href = anchor.getAttribute("href");
      if (!href || !isInternalHref(href)) {
        return;
      }
      const target = anchor.getAttribute("target");
      if (target && target !== "_self") {
        return;
      }
      event.preventDefault();
      router.history.push(href);
    },
    [router]
  );
}
