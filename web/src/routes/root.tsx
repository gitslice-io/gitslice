import {
  HeadContent,
  Outlet,
  Scripts,
  createRootRouteWithContext,
  useRouter,
  useRouterState
} from "@tanstack/react-router";
import {
  HydrationBoundary,
  QueryClient,
  QueryClientProvider,
  type DehydratedState
} from "@tanstack/react-query";
import type { ReactNode } from "react";

import appCss from "../index.css?url";
import { PostHogProvider } from "../analytics/PostHogProvider";
import { ClerkAuthProvider } from "../auth/ClerkAuthProvider";
import { clerkScriptPreload } from "../auth/clerkScript";
import { RpcError } from "../api/rpcError";
import { CLERK_UI_ATTRIBUTE, clerkUiInDocument, clerkUiWanted } from "../lib/clerkUi";
import { SITE_DESCRIPTION } from "../lib/socialMeta";
import { THEME_BOOTSTRAP_SCRIPT } from "../theme";

export interface RouterContext {
  getDehydratedQueryState: () => DehydratedState | undefined;
  queryClient: QueryClient;
}

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 15_000,
        // SSR route loaders await ensureQueryData before the HTML is flushed.
        // react-query's default retry waits ~1s before the single retry, so a
        // failing prefetch (e.g. an RPC that 401s for a signed-out request)
        // blocks the document response for a full second. These prefetches are
        // best-effort — the component refetches client-side on a miss — so we
        // never retry during SSR. The browser keeps one retry.
        // Client errors (401/403/404, e.g. the optimistic readFile on a path
        // that turns out to be a directory) will not succeed on retry, so only
        // transient failures get the single browser retry.
        retry: import.meta.env.SSR
          ? false
          : (failureCount, error) =>
              failureCount < 1 &&
              !(error instanceof RpcError && error.status >= 400 && error.status < 500),
        refetchOnWindowFocus: false
      }
    }
  });
}

// Single-pass undefined-stripping serializer for SSR DehydratedState, avoiding
// a full JSON.parse(JSON.stringify(...)) round-trip while keeping seroval
// hydration clean.
export function stripUndefined<T>(value: T): T {
  if (value === null || typeof value !== "object") {
    return value;
  }
  if (value instanceof Date) {
    return value.toISOString() as unknown as T;
  }
  if (Array.isArray(value)) {
    return value.map((item) => (item === undefined ? null : stripUndefined(item))) as unknown as T;
  }
  const out: Record<string, unknown> = {};
  for (const [key, entry] of Object.entries(value as Record<string, unknown>)) {
    if (entry !== undefined) {
      out[key] = stripUndefined(entry);
    }
  }
  return out as T;
}

export const rootRoute = createRootRouteWithContext<RouterContext>()({
  // Whether this document asks Clerk for its UI script (lib/clerkUi). The
  // server decides from the address and the cookies; the browser keeps to
  // what the document was served with.
  loader: async ({ location }) => {
    if (import.meta.env.SSR) {
      const { getRequestHeader } = await import("@tanstack/react-start/server");
      return { clerkUi: clerkUiWanted(location.pathname, getRequestHeader("cookie") ?? "") };
    }
    return { clerkUi: clerkUiInDocument() };
  },
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1.0" },
      { name: "theme-color", content: "#f8fafc" },
      { title: "Gitslice" },
      // Defaults for link previews; a slice page sets its own (lib/socialMeta).
      { name: "description", content: SITE_DESCRIPTION },
      { property: "og:site_name", content: "Gitslice" },
      { property: "og:title", content: "Gitslice" },
      { property: "og:description", content: SITE_DESCRIPTION },
      { name: "twitter:card", content: "summary" }
    ],
    links: [
      { rel: "stylesheet", href: appCss },
      // Start fetching Clerk's script with the page (auth/clerkScript).
      ...[clerkScriptPreload(import.meta.env.VITE_CLERK_PUBLISHABLE_KEY)].filter((link) => link !== undefined),
      { rel: "icon", type: "image/png", sizes: "64x64", href: "/favicon.png" },
      { rel: "apple-touch-icon", sizes: "180x180", href: "/apple-touch-icon.png" },
      { rel: "manifest", href: "/site.webmanifest" },
      // Plain-text guide for agents: install gs and sign up without a browser.
      { rel: "alternate", type: "text/plain", title: "llms.txt", href: "/llms.txt" }
    ]
  }),
  shellComponent: RootDocument,
  component: () => <Outlet />
});

function RootDocument({ children }: { children: ReactNode }) {
  const router = useRouter();
  const { getDehydratedQueryState, queryClient } = router.options
    .context as RouterContext;
  const rootData = useRouterState({
    select: (state) => state.matches.find((match) => match.routeId === rootRoute.id)?.loaderData
  }) as { clerkUi?: boolean } | undefined;
  // Unknown (an error before the loader ran) means load it.
  const clerkUi = rootData?.clerkUi ?? clerkUiInDocument();

  return (
    <html lang="en" suppressHydrationWarning {...{ [CLERK_UI_ATTRIBUTE]: clerkUi ? "1" : "0" }}>
      <head>
        <HeadContent />
        <script dangerouslySetInnerHTML={{ __html: THEME_BOOTSTRAP_SCRIPT }} />
      </head>
      <body>
        <PostHogProvider />
        <ClerkAuthProvider ui={clerkUi}>
          <QueryClientProvider client={queryClient}>
            <HydrationBoundary state={getDehydratedQueryState()}>
              {children}
            </HydrationBoundary>
          </QueryClientProvider>
        </ClerkAuthProvider>
        <Scripts />
      </body>
    </html>
  );
}
