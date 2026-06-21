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
import { SelectionProvider, useSelection } from "../state/selection";
import { ChangesetDetailPage } from "./ChangesetDetailPage";
import { ChangesetsPage } from "./ChangesetsPage";
import { ChooseUsernamePage } from "./ChooseUsernamePage";
import { CliLoginPage } from "./CliLoginPage";
import { DocPage } from "./DocPage";
import { HomePage } from "./HomePage";
import { LoginPage } from "./LoginPage";
import { SliceCreatePage } from "./SliceCreatePage";
import { SliceDetailPage } from "./SliceDetailPage";
import { SliceSettingsPage } from "./SliceSettingsPage";
import { SlicesPage } from "./SlicesPage";
import { StackCreatePage } from "./StackCreatePage";
import { StackDetailPage } from "./StackDetailPage";
import { StackRestackPage } from "./StackRestackPage";
import { StackSubmitPage } from "./StackSubmitPage";
import { StacksPage } from "./StacksPage";
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

const cliLoginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/cli-login",
  component: CliLoginPage
});

const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
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
  component: SlicesPage
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
        await context.queryClient.ensureQueryData({
          queryKey: ["sliceRef", params.account, params.slice],
          queryFn: () => api.resolveSlice({ ref })
        });
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
        await context.queryClient.ensureQueryData({
          queryKey: ["sliceRef", params.account, params.slice],
          queryFn: () => api.resolveSlice({ ref })
        });
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: SliceSettingsPage
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

const stacksRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "dependencies",
  loaderDeps: ({ search }) => ({
    slice: (search as { slice?: unknown }).slice,
    status: (search as { status?: unknown }).status
  }),
  loader: async ({ context, deps }) => {
    if (import.meta.env.SSR) {
      const sliceRef = parseSliceSearch(deps.slice);
      if (sliceRef) {
        const status = typeof deps.status === "string" ? deps.status : "";
        try {
          const { createServerApiClient } = await import("../api/serverApi");
          const api = await createServerApiClient();
          const { account, slice } = sliceRef;
          await context.queryClient.ensureQueryData({
            queryKey: ["stacks", account, slice, status],
            queryFn: () =>
              api.listStacks({
                authoringSlice: sliceRef,
                status,
                limit: 100
              })
          });
        } catch {
          // The component keeps the existing client-side load/error behavior.
        }
      }
    }
  },
  component: StacksPage
});

const stackCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "dependencies/new",
  component: StackCreatePage
});

const stackDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "dependencies/$id",
  loader: async ({ context, params }) => {
    if (import.meta.env.SSR && params.id) {
      try {
        const { createServerApiClient } = await import("../api/serverApi");
        const api = await createServerApiClient();
        await context.queryClient.ensureQueryData({
          queryKey: ["stack", params.id],
          queryFn: () => api.getStack({ stackId: params.id })
        });
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: StackDetailPage
});

const stackRestackRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "dependencies/$id/update",
  component: StackRestackPage
});

const stackSubmitRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "dependencies/$id/submit",
  component: StackSubmitPage
});

// Primary, shareable changeset URL: /cs/<short changeset id>.
const changesetShortRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "cs/$id",
  loader: async ({ context, params }) => {
    if (import.meta.env.SSR) {
      try {
        const { createServerApiClient } = await import("../api/serverApi");
        const api = await createServerApiClient();
        await context.queryClient.ensureQueryData({
          queryKey: ["changeset", params.id],
          queryFn: () => api.getChangeset({ changesetId: params.id })
        });
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: ChangesetDetailPage
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  cliLoginRoute,
  appRoute.addChildren([
    homeRoute,
    docRoute,
    docSectionRoute,
    slicesRoute,
    sliceCreateRoute,
    sliceSettingsRoute,
    stacksRoute,
    stackCreateRoute,
    stackDetailRoute,
    stackRestackRoute,
    stackSubmitRoute
  ]),
  publicAppRoute.addChildren([
    sliceDetailRoute,
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
