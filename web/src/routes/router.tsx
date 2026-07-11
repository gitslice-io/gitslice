import {
  HeadContent,
  Outlet,
  Scripts,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  useRouter
} from "@tanstack/react-router";
import {
  HydrationBoundary,
  QueryClient,
  QueryClientProvider,
  dehydrate,
  type DehydratedState
} from "@tanstack/react-query";
import type { ReactNode } from "react";

import appCss from "../index.css?url";
import { ClerkAuthProvider } from "../auth/ClerkAuthProvider";
import { RequireAuth } from "../auth/RequireAuth";
import { AppShell } from "../components/AppShell";
import { GLOBAL_REF_NAME } from "../lib/globalRef";
import { SelectionProvider, useSelection } from "../state/selection";
import { ChangesetDetailPage, sortedPatchsets } from "./ChangesetDetailPage";
import { ChangesetsPage } from "./ChangesetsPage";
import { ChooseUsernamePage } from "./ChooseUsernamePage";
import { CliLoginPage } from "./CliLoginPage";
import { ConversationsPage } from "./ConversationsPage";
import { DocPage } from "./DocPage";
import { HomePage } from "./HomePage";
import { LoginPage } from "./LoginPage";
import { SliceCreatePage } from "./SliceCreatePage";
import { SliceAgentsPage } from "./SliceAgentsPage";
import { SliceDetailPage } from "./SliceDetailPage";
import { SliceSettingsPage } from "./SliceSettingsPage";
import { parseSliceSearch } from "./stackPageUtils";

interface RouterContext {
  getDehydratedQueryState: () => DehydratedState | undefined;
  queryClient: QueryClient;
}

function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 15_000,
        retry: 1,
        refetchOnWindowFocus: false
      }
    }
  });
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1.0" },
      { title: "Gitslice" }
    ],
    links: [{ rel: "stylesheet", href: appCss }]
  }),
  shellComponent: RootDocument,
  component: () => <Outlet />
});

function RootDocument({ children }: { children: ReactNode }) {
  const router = useRouter();
  const { getDehydratedQueryState, queryClient } = router.options
    .context as RouterContext;

  return (
    <html lang="en">
      <head>
        <HeadContent />
      </head>
      <body>
        <ClerkAuthProvider>
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

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: LoginPage
});

const loginFlowRoute = createRoute({
  getParentRoute: () => rootRoute,
  // Clerk's path-based sign-in flow uses nested paths for OAuth callbacks and
  // other verification steps (for example, /login/sso-callback). Keep the
  // whole flow mounted on the same component instead of treating those paths
  // as application 404s.
  path: "/login/$",
  component: LoginPage
});

const cliLoginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/cli-login",
  component: CliLoginPage
});

const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  loader: async ({ context }) => {
    if (import.meta.env.SSR) {
      try {
        const { auth } = await import("@clerk/tanstack-react-start/server");
        const authState = await auth();
        // Signed-out visitors are redirected to /login by RequireAuth; skip
        // the RPC that would be guaranteed to fail without a session token.
        if (!authState.isAuthenticated) return;
        const { createServerApiClient } = await import("../api/serverApi");
        const api = await createServerApiClient();
        await context.queryClient.ensureQueryData({
          queryKey: ["authStatus"],
          queryFn: () => api.getAuthStatus({})
        });
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: () => (
    <RequireAuth>
      <SelectionProvider>
        <UsernameGate>
          <AppShell />
        </UsernameGate>
      </SelectionProvider>
    </RequireAuth>
  )
});

const publicAppRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "publicApp",
  component: () => (
    <SelectionProvider>
      <AppShell />
    </SelectionProvider>
  )
});

function UsernameGate({ children }: { children: ReactNode }) {
  const { error, isLoading, needsUsername } = useSelection();

  if (isLoading) {
    return (
      <main className="grid min-h-[100dvh] place-items-center bg-slate-50 p-6 text-sm text-slate-600">
        Loading session...
      </main>
    );
  }

  if (error) {
    return (
      <main className="grid min-h-[100dvh] place-items-center bg-slate-50 p-6 text-sm text-slate-600">
        <section className="w-full max-w-md rounded-lg border border-rose-200 bg-white p-5 text-rose-900 shadow-sm shadow-slate-200/50">
          <p className="font-semibold">Could not load session</p>
          <p className="mt-2 leading-6">{error.message}</p>
        </section>
      </main>
    );
  }

  if (needsUsername) {
    return <ChooseUsernamePage />;
  }

  return <>{children}</>;
}

const homeRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  component: HomePage
});

const conversationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "conversations",
  component: ConversationsPage
});

const docRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "doc",
  component: DocPage
});

const docSectionRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "doc/$section",
  component: DocPage
});

const slicesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "slices",
  component: HomePage
});

const sliceCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "slices/new",
  component: SliceCreatePage
});

const sliceDetailRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "slices/$account/$slice",
  loader: async ({ context, params }) => {
    if (import.meta.env.SSR && params.account && params.slice) {
      try {
        const { createServerApiClient } = await import("../api/serverApi");
        const api = await createServerApiClient();
        const ref = { account: params.account, slice: params.slice };
        // The default view resolves the slice and the latest global ref (which
        // yields the commit the file tree renders from); both fire on first
        // paint and have no derived inputs, so prefetch them together.
        await Promise.all([
          context.queryClient.ensureQueryData({
            queryKey: ["sliceRef", params.account, params.slice],
            queryFn: () => api.resolveSlice({ ref })
          }),
          context.queryClient.ensureQueryData({
            queryKey: ["globalRef", GLOBAL_REF_NAME],
            queryFn: async () => {
              const latest = await api.getRef({ refName: GLOBAL_REF_NAME });
              if (!latest.commitId) {
                throw new Error("Latest global state did not return a commit id.");
              }
              return latest;
            }
          })
        ]);
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: SliceDetailPage
});

const sliceSettingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "slices/$account/$slice/settings",
  loader: async ({ context, params }) => {
    if (import.meta.env.SSR && params.account && params.slice) {
      try {
        const { createServerApiClient } = await import("../api/serverApi");
        const api = await createServerApiClient();
        const ref = { account: params.account, slice: params.slice };
        // Settings resolves the slice and (in the definition form) the latest
        // global ref; both render on first paint.
        await Promise.all([
          context.queryClient.ensureQueryData({
            queryKey: ["sliceRef", params.account, params.slice],
            queryFn: () => api.resolveSlice({ ref })
          }),
          context.queryClient.ensureQueryData({
            queryKey: ["globalRef", GLOBAL_REF_NAME],
            queryFn: () => api.getRef({ refName: GLOBAL_REF_NAME })
          })
        ]);
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: SliceSettingsPage
});

const sliceAgentsRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "slices/$account/$slice/agents",
  component: SliceAgentsPage
});

const sliceAgentConversationRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "slices/$account/$slice/agents/$conversationId",
  loader: async ({ context, params }) => {
    if (import.meta.env.SSR && params.conversationId) {
      try {
        const { createServerApiClient } = await import("../api/serverApi");
        const api = await createServerApiClient();
        await context.queryClient.ensureQueryData({
          queryKey: ["conversation", params.conversationId],
          queryFn: () =>
            api.getConversation({ conversationId: params.conversationId })
        });
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: SliceAgentsPage
});

// Public, shareable changeset list URL: /changesets?slice=<account:slice>.
// Readable anonymously for public slices; write actions stay gated behind auth.
const changesetsRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "changesets",
  loaderDeps: ({ search }) => ({
    slice: (search as { slice?: unknown }).slice
  }),
  loader: async ({ context, deps }) => {
    if (import.meta.env.SSR) {
      const sliceRef = parseSliceSearch(deps.slice);
      if (sliceRef) {
        try {
          const { createServerApiClient } = await import("../api/serverApi");
          const api = await createServerApiClient();
          const { account, slice } = sliceRef;
          await context.queryClient.ensureQueryData({
            queryKey: ["changesets", account, slice],
            queryFn: () =>
              api.listChangesets({ authoringSlice: { account, slice } })
          });
        } catch {
          // The component keeps the existing client-side load/error behavior.
        }
      }
    }
  },
  component: ChangesetsPage
});

// Primary, shareable changeset URL: /cs/<short changeset id>.
const changesetShortRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "cs/$id",
  loaderDeps: ({ search }) => ({
    from: (search as { from?: unknown }).from,
    to: (search as { to?: unknown }).to
  }),
  loader: async ({ context, deps, params }) => {
    if (import.meta.env.SSR) {
      try {
        const { createServerApiClient } = await import("../api/serverApi");
        const api = await createServerApiClient();
        const changeset = await context.queryClient.ensureQueryData({
          queryKey: ["changeset", params.id],
          queryFn: () => api.getChangeset({ changesetId: params.id })
        });
        // The diff is the page's primary content; the slice's changesets back
        // the dependent-changeset list. Both render on first paint and derive
        // only from the changeset just fetched, so prefill them under the same
        // keys the component uses.
        const canonicalChangesetId = changeset.id || params.id;
        const patchsets = sortedPatchsets(changeset);
        const ids = new Set(
          patchsets
            .map((patchset) => patchset.id)
            .filter((id): id is string => Boolean(id))
        );
        // Mirror the component's URL-driven from/to resolution so the prefilled
        // diff matches the shared link and the page renders it without a refetch.
        const fromPatchset =
          typeof deps.from === "string" && ids.has(deps.from) ? deps.from : "";
        const toPatchset =
          typeof deps.to === "string" && ids.has(deps.to)
            ? deps.to
            : changeset.currentPatchsetId ||
              patchsets[patchsets.length - 1]?.id ||
              "";
        const authoringSlice = changeset.authoringSlice;
        await Promise.all([
          context.queryClient.ensureQueryData({
            queryKey: [
              "changesetDiff",
              canonicalChangesetId,
              fromPatchset,
              toPatchset
            ],
            queryFn: () =>
              api.diffChangeset({
                changesetId: canonicalChangesetId,
                fromPatchset: fromPatchset || undefined,
                toPatchset: toPatchset || undefined
              })
          }),
          authoringSlice?.account && authoringSlice?.slice
            ? context.queryClient.ensureQueryData({
                queryKey: [
                  "changesetsBySlice",
                  authoringSlice.account,
                  authoringSlice.slice
                ],
                queryFn: () =>
                  api.listChangesets({ authoringSlice, limit: 200 })
              })
            : Promise.resolve(undefined)
        ]);
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: ChangesetDetailPage
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  loginFlowRoute,
  cliLoginRoute,
  appRoute.addChildren([
    homeRoute,
    conversationsRoute,
    docRoute,
    docSectionRoute,
    slicesRoute,
    sliceCreateRoute,
    sliceSettingsRoute
  ]),
  publicAppRoute.addChildren([
    sliceDetailRoute,
    sliceAgentsRoute,
    sliceAgentConversationRoute,
    changesetsRoute,
    changesetShortRoute
  ])
]);

export function getRouter() {
  const queryClient = createQueryClient();
  let dehydratedQueryState: DehydratedState | undefined;

  return createRouter({
    routeTree,
    context: {
      getDehydratedQueryState: () => dehydratedQueryState,
      queryClient
    },
    defaultPreload: "intent",
    defaultPreloadStaleTime: 0,
    scrollRestoration: true,
    dehydrate: () => {
      dehydratedQueryState = dehydrate(queryClient, {
        shouldDehydrateMutation: () => false
      });
      return {
        queryClient: JSON.parse(JSON.stringify(dehydratedQueryState))
      };
    },
    hydrate: (dehydrated) => {
      dehydratedQueryState = dehydrated?.queryClient;
    }
  });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof getRouter>;
  }
}

declare module "@tanstack/react-start" {
  interface Register {
    ssr: true;
    router: ReturnType<typeof getRouter>;
  }
}
