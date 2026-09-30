import {
  HeadContent,
  Outlet,
  Scripts,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  lazyRouteComponent,
  useRouter
} from "@tanstack/react-router";
import {
  HydrationBoundary,
  QueryClient,
  QueryClientProvider,
  dehydrate,
  type DehydratedState
} from "@tanstack/react-query";
import { useAuth } from "@clerk/tanstack-react-start";
import { Suspense, type ReactNode } from "react";

import appCss from "../index.css?url";
import { PostHogProvider } from "../analytics/PostHogProvider";
import { ClerkAuthProvider } from "../auth/ClerkAuthProvider";
import { RpcError } from "../api/errors";
import {
  accountSlicesQuery,
  authStatusQuery,
  dependentCandidatesQuery,
  ownedAgentsQuery,
  pendingClaimsQuery,
  recentConversationsQuery,
  sliceChangesetsQuery
} from "../api/queries";
import {
  sliceDirectoryQueryKey,
  sliceFileQueryKey,
  slicePathQueryKey
} from "../lib/sliceQueryKeys";
import {
  isSliceProjectionDirectoryPath,
  listDirectoryAll
} from "../components/source/sourceUtils";
import { NavigationProgress } from "../components/NavigationProgress";
import { initialTreeExpansion, pathSearchValue } from "./slice-detail/sourceTree";
import { GLOBAL_REF_NAME } from "../lib/globalRef";
import { THEME_BOOTSTRAP_SCRIPT } from "../theme";
import {
  FULL_DIFF_PATH_LIMIT,
  changedPathsForDiff,
  sortedPatchsets
} from "./changeset-detail/patchsetUtils";
import { parseSliceSearch } from "./stackPageUtils";

interface RouterContext {
  getDehydratedQueryState: () => DehydratedState | undefined;
  queryClient: QueryClient;
}

const BlogListPage = lazyRouteComponent(
  () => import("./BlogListPage"),
  "BlogListPage"
);
const BlogPostPage = lazyRouteComponent(
  () => import("./BlogPostPage"),
  "BlogPostPage"
);
const ChangesetDetailPage = lazyRouteComponent(
  () => import("./ChangesetDetailPage"),
  "ChangesetDetailPage"
);
const ChangesetsPage = lazyRouteComponent(
  () => import("./ChangesetsPage"),
  "ChangesetsPage"
);
const CliLoginPage = lazyRouteComponent(
  () => import("./CliLoginPage"),
  "CliLoginPage"
);
const ClaimsPage = lazyRouteComponent(() => import("./ClaimsPage"), "ClaimsPage");
const ConversationsPage = lazyRouteComponent(
  () => import("./ConversationsPage"),
  "ConversationsPage"
);
const DocPage = lazyRouteComponent(() => import("./DocPage"), "DocPage");
const HomePage = lazyRouteComponent(() => import("./HomePage"), "HomePage");
const LandingPage = lazyRouteComponent(
  () => import("./LandingPage"),
  "LandingPage"
);
const LoginPage = lazyRouteComponent(() => import("./LoginPage"), "LoginPage");
const SliceCreatePage = lazyRouteComponent(
  () => import("./SliceCreatePage"),
  "SliceCreatePage"
);
const SliceAgentsPage = lazyRouteComponent(
  () => import("./SliceAgentsPage"),
  "SliceAgentsPage"
);
const SliceDetailPage = lazyRouteComponent(
  () => import("./SliceDetailPage"),
  "SliceDetailPage"
);
const SliceSettingsPage = lazyRouteComponent(
  () => import("./SliceSettingsPage"),
  "SliceSettingsPage"
);
const AuthedAppLayout = lazyRouteComponent(
  () => import("./AppLayouts"),
  "AuthedAppLayout"
);
const PublicAppLayout = lazyRouteComponent(
  () => import("./AppLayouts"),
  "PublicAppLayout"
);
const SignedInHome = lazyRouteComponent(
  () => import("./AppLayouts"),
  "SignedInHome"
);

const DOC_SECTION_TITLES: Record<string, string> = {
  start: "Start Here",
  concepts: "Concepts",
  agents: "Agents",
  checks: "CI Checks",
  "git-users": "For Git Users",
  cli: "CLI Reference"
};

export function docSectionTitle(section?: string) {
  const title = section ? DOC_SECTION_TITLES[section] : undefined;
  return title ? `${title} · Docs · Gitslice` : "Docs · Gitslice";
}

export function sliceTitle(account: string, slice: string, path?: unknown) {
  const selectedPath = pathSearchValue(path);
  const pathName = selectedPath.split("/").filter(Boolean).pop();
  const sliceLabel = `${account}:${slice}`;
  return pathName
    ? `${pathName} · ${sliceLabel} · Gitslice`
    : `${sliceLabel} · Gitslice`;
}

export function changesetTitle(id: string, title?: string) {
  const changesetLabel = `Changeset ${id}`;
  return title
    ? `${title} · ${changesetLabel} · Gitslice`
    : `${changesetLabel} · Gitslice`;
}

export function hasDehydratedAuthStatus(state: DehydratedState | undefined) {
  return Boolean(
    state?.queries.some(
      (query) =>
        query.queryKey.length === 1 &&
        query.queryKey[0] === "authStatus" &&
        query.state.status === "success"
    )
  );
}

// Client-only: the dehydrated query state from SSR, and whether the index
// route's first (hydration) preload has run. See IndexPage.preload.
let clientDehydratedQueryState: DehydratedState | undefined;
let indexChunkPreloaded = false;

function createQueryClient() {
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

const rootRoute = createRootRouteWithContext<RouterContext>()({
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1.0" },
      { name: "theme-color", content: "#f8fafc" },
      { title: "Gitslice" }
    ],
    links: [
      { rel: "stylesheet", href: appCss },
      { rel: "icon", type: "image/png", sizes: "64x64", href: "/favicon.png" },
      {
        rel: "apple-touch-icon",
        sizes: "180x180",
        href: "/apple-touch-icon.png"
      },
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

  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        <HeadContent />
        <script dangerouslySetInnerHTML={{ __html: THEME_BOOTSTRAP_SCRIPT }} />
      </head>
      <body>
        <NavigationProgress />
        <PostHogProvider />
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
  head: () => ({ meta: [{ title: "Sign in · Gitslice" }] }),
  component: LoginPage
});

const loginFlowRoute = createRoute({
  getParentRoute: () => rootRoute,
  // Clerk's path-based sign-in flow uses nested paths for OAuth callbacks and
  // other verification steps (for example, /login/sso-callback). Keep the
  // whole flow mounted on the same component instead of treating those paths
  // as application 404s.
  path: "/login/$",
  head: () => ({ meta: [{ title: "Sign in · Gitslice" }] }),
  component: LoginPage
});

const cliLoginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/cli-login",
  head: () => ({ meta: [{ title: "Authorize the CLI · Gitslice" }] }),
  component: CliLoginPage
});

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  loader: async ({ context }) => {
    if (import.meta.env.SSR) {
      try {
        const { auth } = await import("@clerk/tanstack-react-start/server");
        const authState = await auth();
        // Signed-out visitors render the public landing page; skip the RPC
        // that would be guaranteed to fail without a session token.
        if (!authState.isAuthenticated) return;
        const { createServerApiClient } = await import("../api/serverApi");
        const api = await createServerApiClient();
        const authStatus = await context.queryClient.ensureQueryData(
          authStatusQuery(api)
        );
        const account = authStatus.accounts?.[0] ?? "";
        if (!authStatus.needsUsername && account) {
          const ownedAgents = context.queryClient.ensureQueryData(
            ownedAgentsQuery(api)
          );
          const prefetches = [
            ownedAgents.then((agents) => {
              const agentAccounts = new Set(
                agents
                  .map((agent) => agent.account ?? "")
                  .filter(
                    (agentAccount) =>
                      Boolean(agentAccount) && agentAccount !== account
                  )
              );
              return Promise.allSettled(
                Array.from(agentAccounts, (agentAccount) =>
                  context.queryClient.ensureQueryData(
                    accountSlicesQuery(api, agentAccount)
                  )
                )
              );
            }),
            context.queryClient.ensureQueryData(pendingClaimsQuery(api)),
            context.queryClient.ensureQueryData(recentConversationsQuery(api)),
            context.queryClient.ensureQueryData(accountSlicesQuery(api, account))
          ];
          await Promise.race([
            Promise.allSettled(prefetches),
            new Promise((resolve) => setTimeout(resolve, 900))
          ]);
        }
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: IndexPage
});

function IndexPage() {
  const { isLoaded, isSignedIn } = useAuth();

  const Page = isLoaded && isSignedIn ? SignedInHome : LandingPage;

  return (
    <Suspense fallback={<SessionLoadingFallback />}>
      <Page />
    </Suspense>
  );
}

function SessionLoadingFallback() {
  return (
    <main className="grid min-h-[100dvh] place-items-center bg-slate-50 dark:bg-zinc-950 p-6 text-sm text-slate-600 dark:text-zinc-400">
      Loading session...
    </main>
  );
}

// "/" renders one of two lazy chunks depending on auth. They are nested inside
// the route component, so the router only preloads them through this hook. On
// the first client call (hydration) load just the chunk the server rendered:
// the SSR index loader dehydrates authStatus only for signed-in requests, so
// signed-out visitors never download the app shell or the API client. Later
// calls (client navigations to "/") load both.
IndexPage.preload = () => {
  if (!import.meta.env.SSR && !indexChunkPreloaded) {
    indexChunkPreloaded = true;
    const page = hasDehydratedAuthStatus(clientDehydratedQueryState)
      ? SignedInHome
      : LandingPage;
    return page.preload?.() ?? Promise.resolve();
  }

  return Promise.all([LandingPage.preload?.(), SignedInHome.preload?.()]).then(
    () => undefined
  );
};

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
        await context.queryClient.ensureQueryData(authStatusQuery(api));
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: AuthedAppLayout
});

const publicAppRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "publicApp",
  component: PublicAppLayout
});

const claimsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "claims",
  head: () => ({ meta: [{ title: "Claim agents · Gitslice" }] }),
  component: ClaimsPage
});

const conversationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "conversations",
  head: () => ({ meta: [{ title: "Conversations · Gitslice" }] }),
  component: ConversationsPage
});

// Public docs: reachable from the signed-out landing page.
const docRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "doc",
  head: () => ({ meta: [{ title: "Docs · Gitslice" }] }),
  component: DocPage
});

const docSectionRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "doc/$section",
  head: ({ params }) => ({ meta: [{ title: docSectionTitle(params.section) }] }),
  component: DocPage
});

const blogsRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "blogs",
  head: () => ({ meta: [{ title: "Blog · Gitslice" }] }),
  component: BlogListPage
});

const blogPostRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "blogs/$slug",
  loader: async ({ params }) => {
    const { getPost } = await import("./blog/posts");
    return { title: getPost(params.slug)?.title ?? "" };
  },
  head: ({ loaderData }) => ({
    meta: [{ title: loaderData?.title || "Blog · Gitslice" }]
  }),
  component: BlogPostPage
});

const slicesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "slices",
  head: () => ({ meta: [{ title: "Slices · Gitslice" }] }),
  component: HomePage
});

const sliceCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "slices/new",
  head: () => ({ meta: [{ title: "New slice · Gitslice" }] }),
  component: SliceCreatePage
});

// Upper bound on how long SSR waits for path-specific prefetches. Anything that
// finishes in time is hydrated; the rest loads client-side as before, so a slow
// API can delay the first byte by at most this much.
const SLICE_PATH_PREFETCH_BUDGET_MS = 1200;

const sliceDetailRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "slices/$account/$slice",
  loaderDeps: ({ search }) => ({
    path: pathSearchValue((search as { path?: unknown }).path)
  }),
  head: ({ match, params }) => ({
    meta: [
      {
        title: sliceTitle(params.account, params.slice, match.loaderDeps.path)
      }
    ]
  }),
  loader: async ({ context, deps, params }) => {
    if (import.meta.env.SSR && params.account && params.slice) {
      try {
        const { createServerApiClient } = await import("../api/serverApi");
        const api = await createServerApiClient();
        const ref = { account: params.account, slice: params.slice };
        // The default view resolves the slice and the latest global ref (which
        // yields the commit the file tree renders from); both fire on first
        // paint and have no derived inputs, so prefetch them together.
        const [slice, latest] = await Promise.all([
          context.queryClient.ensureQueryData({
            queryKey: ["sliceRef", params.account, params.slice],
            queryFn: () => api.resolveSlice({ ref })
          }),
          context.queryClient.ensureQueryData({
            queryKey: ["globalRef", GLOBAL_REF_NAME],
            queryFn: async () => {
              const latest = await api.getRef({ refName: GLOBAL_REF_NAME });
              if (!latest.commitId) {
                throw new Error(
                  "Latest global state did not return a commit id."
                );
              }
              return latest;
            }
          })
        ]);
        const commitId = latest.commitId ?? "";
        const sliceRef = slice.ref ?? ref;
        const includedPaths = slice.definition?.includedPaths ?? [];
        const selectedPath = deps.path;
        if (commitId) {
          // Without this, the browser discovers the path's kind, then its
          // listing, then the navigator's ancestor listings, one round trip at
          // a time. Prefetch them here with the page's own query keys.
          const listing = (path: string) =>
            context.queryClient.ensureQueryData({
              queryKey: sliceDirectoryQueryKey(sliceRef, commitId, path),
              queryFn: () =>
                listDirectoryAll(api, {
                  allowMissingDirectory: isSliceProjectionDirectoryPath(path, includedPaths),
                  commitId,
                  path,
                  slice: sliceRef
                })
            });
          const navigatorPaths = new Set(
            initialTreeExpansion(selectedPath, includedPaths, false)
          );
          const work: Promise<unknown>[] = Array.from(navigatorPaths, listing);
          if (selectedPath && !isSliceProjectionDirectoryPath(selectedPath, includedPaths)) {
            work.push(
              context.queryClient
                .ensureQueryData({
                  queryKey: slicePathQueryKey(sliceRef, commitId, selectedPath),
                  queryFn: () =>
                    api.resolvePath({ commitId, path: selectedPath, slice: sliceRef })
                })
                .then((resolved): Promise<unknown> | undefined => {
                  const kind = resolved.entry?.kind;
                  if (kind === "ENTRY_KIND_DIRECTORY") {
                    return listing(selectedPath);
                  }
                  if (kind === "ENTRY_KIND_FILE") {
                    return context.queryClient.ensureQueryData({
                      queryKey: sliceFileQueryKey(sliceRef, commitId, selectedPath),
                      queryFn: () =>
                        api.readFile({ commitId, path: selectedPath, slice: sliceRef })
                    });
                  }
                  return undefined;
                })
            );
          } else if (selectedPath) {
            work.push(listing(selectedPath));
          }
          await Promise.race([
            Promise.allSettled(work),
            new Promise((resolve) => setTimeout(resolve, SLICE_PATH_PREFETCH_BUDGET_MS))
          ]);
        }
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
  head: ({ params }) => ({
    meta: [
      {
        title: `Settings · ${params.account}:${params.slice} · Gitslice`
      }
    ]
  }),
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
  head: ({ params }) => ({
    meta: [
      {
        title: `Conversations · ${params.account}:${params.slice} · Gitslice`
      }
    ]
  }),
  component: SliceAgentsPage
});

const sliceAgentConversationRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "slices/$account/$slice/agents/$conversationId",
  head: ({ params }) => ({
    meta: [
      {
        title: `Conversations · ${params.account}:${params.slice} · Gitslice`
      }
    ]
  }),
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
  head: ({ match }) => {
    const slice = match.loaderDeps.slice;
    return {
      meta: [
        {
          title:
            typeof slice === "string" && slice
              ? `Changesets · ${slice} · Gitslice`
              : "Changesets · Gitslice"
        }
      ]
    };
  },
  loader: async ({ context, deps }) => {
    if (import.meta.env.SSR) {
      const sliceRef = parseSliceSearch(deps.slice);
      if (sliceRef) {
        try {
          const { createServerApiClient } = await import("../api/serverApi");
          const api = await createServerApiClient();
          const { account, slice } = sliceRef;
          await context.queryClient.ensureQueryData(
            sliceChangesetsQuery(api, account, slice)
          );
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
        // Past FULL_DIFF_PATH_LIMIT paths the page switches to per-file diffs
        // and never reads the full diff, so fetching it here would only slow
        // the response and bloat the HTML with state nobody uses.
        const usesFullDiff =
          changedPathsForDiff(
            patchsets.find((patchset) => patchset.id === fromPatchset),
            patchsets.find((patchset) => patchset.id === toPatchset)
          ).length <= FULL_DIFF_PATH_LIMIT;
        await Promise.all([
          usesFullDiff
            ? context.queryClient.ensureQueryData({
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
              })
            : Promise.resolve(undefined),
          authoringSlice?.account && authoringSlice?.slice
            ? context.queryClient.ensureQueryData(
                dependentCandidatesQuery(api, authoringSlice)
              )
            : Promise.resolve(undefined)
        ]);
        return { title: changeset.title ?? "" };
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  head: ({ loaderData, params }) => ({
    meta: [{ title: changesetTitle(params.id, loaderData?.title) }]
  }),
  component: ChangesetDetailPage
});

const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
  loginFlowRoute,
  cliLoginRoute,
  appRoute.addChildren([
    claimsRoute,
    conversationsRoute,
    slicesRoute,
    sliceCreateRoute,
    sliceSettingsRoute
  ]),
  publicAppRoute.addChildren([
    docRoute,
    docSectionRoute,
    blogsRoute,
    blogPostRoute,
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
      // Runs before the router preloads route chunks, so IndexPage.preload can
      // fetch only the chunk the server actually rendered.
      clientDehydratedQueryState = dehydratedQueryState;
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
