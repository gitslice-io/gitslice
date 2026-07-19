import type { PostHogConfig, Properties } from "posthog-js";

import { AnalyticsEvents, AnalyticsProps } from "./events";

type PostHogModule = typeof import("posthog-js");
type PostHogClient = PostHogModule["default"];
type AnalyticsPropName =
  (typeof AnalyticsProps)[keyof typeof AnalyticsProps];

export type WebAnalyticsEventName = typeof AnalyticsEvents.sliceViewed;
export type AnalyticsCaptureProperties = Partial<
  Record<AnalyticsPropName, string | number | boolean | null | undefined>
>;
export type AnalyticsUserProperties = Record<
  string,
  string | number | boolean | null | undefined
>;

const env = import.meta.env as Record<string, string | undefined>;
const posthogKey = env.VITE_POSTHOG_KEY;
const posthogHost = env.VITE_POSTHOG_HOST;

let posthogModulePromise: Promise<PostHogModule> | undefined;
let initialized = false;
let originalDistinctIdForAlias: string | undefined;
let lastAliasKey: string | undefined;

function isPostHogEnabled() {
  return (
    !import.meta.env.SSR && typeof window !== "undefined" && Boolean(posthogKey)
  );
}

async function loadPostHog(): Promise<PostHogClient | undefined> {
  if (!isPostHogEnabled()) {
    return undefined;
  }

  const key = posthogKey;
  if (!key) {
    return undefined;
  }

  try {
    posthogModulePromise ??= import("posthog-js");
    const { default: posthog } = await posthogModulePromise;

    if (!initialized) {
      const config: Partial<PostHogConfig> = {
        capture_pageview: false,
        person_profiles: "identified_only",
      };

      if (posthogHost) {
        config.api_host = posthogHost;
      }

      posthog.init(key, config);
      initialized = true;
    }

    return posthog;
  } catch {
    return undefined;
  }
}

function withPostHog(callback: (posthog: PostHogClient) => void) {
  if (!isPostHogEnabled()) {
    return;
  }

  void loadPostHog()
    .then((posthog) => {
      if (!posthog) {
        return;
      }

      try {
        callback(posthog);
      } catch {
        // Analytics must never break rendering or navigation.
      }
    })
    .catch(() => undefined);
}

export function initPostHog() {
  void loadPostHog();
}

export function capture(
  event: WebAnalyticsEventName,
  props?: AnalyticsCaptureProperties,
) {
  withPostHog((posthog) => {
    posthog.capture(event, props as Properties | undefined);
  });
}

export function capturePageview(path: string) {
  withPostHog((posthog) => {
    const url = new URL(
      path || window.location.pathname,
      window.location.origin,
    );

    posthog.capture("$pageview", {
      $current_url: url.toString(),
      $pathname: url.pathname,
    });
  });
}

export function identifyUser(id: string, props?: AnalyticsUserProperties) {
  if (!id) {
    return;
  }

  withPostHog((posthog) => {
    const currentDistinctId = posthog.get_distinct_id();
    if (currentDistinctId && currentDistinctId !== id) {
      originalDistinctIdForAlias = currentDistinctId;
    }

    posthog.identify(id, props as Properties | undefined);
  });
}

export function aliasUser(id: string) {
  if (!id) {
    return;
  }

  withPostHog((posthog) => {
    const originalDistinctId = originalDistinctIdForAlias;
    const aliasKey = `${originalDistinctId ?? "current"}->${id}`;
    if (lastAliasKey === aliasKey) {
      return;
    }

    if (originalDistinctId && originalDistinctId !== id) {
      posthog.alias(id, originalDistinctId);
      originalDistinctIdForAlias = undefined;
    } else {
      posthog.alias(id);
    }

    lastAliasKey = aliasKey;
  });
}

export function resetUser() {
  withPostHog((posthog) => {
    posthog.reset();
    originalDistinctIdForAlias = undefined;
    lastAliasKey = undefined;
  });
}
