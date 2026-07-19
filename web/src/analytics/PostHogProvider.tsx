import { useEffect, useRef } from "react";
import { useRouter } from "@tanstack/react-router";

import { capturePageview, initPostHog } from "./posthog";

export function PostHogProvider() {
  const router = useRouter();
  const lastPageviewPath = useRef<string>();

  useEffect(() => {
    if (typeof window === "undefined") {
      return;
    }

    initPostHog();

    function captureRoutePageview(path: string) {
      if (lastPageviewPath.current === path) {
        return;
      }

      lastPageviewPath.current = path;
      capturePageview(path);
    }

    captureRoutePageview(router.state.location.href);

    return router.subscribe("onResolved", (event) => {
      if (!event.hrefChanged) {
        return;
      }

      captureRoutePageview(event.toLocation.href);
    });
  }, [router]);

  return null;
}
