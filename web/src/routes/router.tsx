import {
  HeadContent,
  Outlet,
  Scripts,
  createRootRouteWithContext,
  redirect,
  createRoute,
  createRouter,
  isNotFound,
  notFound,
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
import type { ReactNode } from "react";

import appCss from "../index.css?url";
import { PostHogProvider } from "../analytics/PostHogProvider";
import { ClerkAuthProvider } from "../auth/ClerkAuthProvider";
import { RpcError } from "../api/client";
import {
  sliceDirectoryQueryKey,
  sliceFileQueryKey,
  slicePathQueryKey,
  sliceRenderedFileQueryKey
} from "../lib/sliceQueryKeys";
import {
  isSliceProjectionDirectoryPath,
  listDirectoryAll,
  normalizeRepositoryPath
} from "../components/source/sourceUtils";
import { findNestedReadme, nestedReadmeQueryKey, onlyDirectory } from "./slice-detail/nestedReadme";
import { findReadmeEntry, README_MAX_BYTES } from "./slice-detail/readme";
import { initialTreeExpansion, pathSearchValue } from "./slice-detail/sourceTree";
import { RequireAuth } from "../auth/RequireAuth";
import { AppShell } from "../components/AppShell";
import { isNotFoundError } from "../api/client";
import type { Changeset, Slice } from "../api/types";
import { shortChangesetId } from "../lib/objectId";
import { changesetsListQuery, parseStatusTab } from "./changesets-list/filters";
import { GLOBAL_REF_NAME } from "../lib/globalRef";
import { conversationsEnabled } from "../lib/features";
import {
  changesetHead,
  sliceHead,
  SITE_DESCRIPTION,
  type ChangesetSocial,
  type SliceSocial
} from "../lib/socialMeta";
import { SelectionProvider, useSelection } from "../state/selection";
import { THEME_BOOTSTRAP_SCRIPT } from "../theme";
import { BlogListPage } from "./BlogListPage";
import { BlogPostPage } from "./BlogPostPage";
import { AccountPage } from "./AccountPage";
import { ChangesetDetailPage, sortedPatchsets } from "./ChangesetDetailPage";
import { AccountNotFound, ChangesetNotFound, PageNotFound, SliceNotFound } from "./NotFoundPage";
import { ChangesetsPage } from "./ChangesetsPage";
import { ChooseUsernamePage } from "./ChooseUsernamePage";
import { CliLoginPage } from "./CliLoginPage";
import { ClaimsPage } from "./ClaimsPage";
import { ConversationsPage } from "./ConversationsPage";
import { DocPage } from "./DocPage";
import { HomePage } from "./HomePage";
import { LandingPage } from "./LandingPage";
import { LoginPage } from "./LoginPage";
import { SignupPage } from "./SignupPage";
import { NewOrganizationPage } from "./NewOrganizationPage";
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

const signupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/signup",
  component: SignupPage
});

const signupFlowRoute = createRoute({
  getParentRoute: () => rootRoute,
  // Clerk's sign-up flow nests verification steps under the page, as sign-in
  // does under /login.
  path: "/signup/$",
  component: SignupPage
});

const cliLoginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/cli-login",
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
        await context.queryClient.ensureQueryData({
          queryKey: ["authStatus"],
          queryFn: () => api.getAuthStatus({})
        });
      } catch {
        // The component keeps the existing client-side load/error behavior.
      }
    }
  },
  component: IndexPage
});

function IndexPage() {
  const { isLoaded, isSignedIn } = useAuth();

  if (isLoaded && isSignedIn) {
    return (
      <RequireAuth>
        <SelectionProvider>
          <UsernameGate>
            <AppShell>
              <HomePage />
            </AppShell>
          </UsernameGate>
        </SelectionProvider>
      </RequireAuth>
    );
  }

  return <LandingPage />;
}

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
      <main className="grid min-h-[100dvh] place-items-center bg-slate-50 dark:bg-zinc-950 p-6 text-sm text-slate-600 dark:text-zinc-400">
        Loading session...
      </main>
    );
  }

  if (error) {
    return (
      <main className="grid min-h-[100dvh] place-items-center bg-slate-50 dark:bg-zinc-950 p-6 text-sm text-slate-600 dark:text-zinc-400">
        <section className="w-full max-w-md rounded-lg border border-rose-200 dark:border-rose-900/60 bg-white dark:bg-zinc-900 p-5 text-rose-900 dark:text-rose-200 shadow-sm shadow-slate-200/50 dark:shadow-black/50">
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

const claimsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "claims",
  component: ClaimsPage
});

// Conversations are not offered for now (lib/features): their addresses go
// to the home page, or to the slice, instead of showing the feature.
const conversationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "conversations",
  beforeLoad: () => {
    if (!conversationsEnabled) {
      throw redirect({ replace: true, to: "/" });
    }
  },
  component: ConversationsPage
});

// Public docs: reachable from the signed-out landing page.
const docRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "doc",
  component: DocPage
});

const docSectionRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "doc/$section",
  component: DocPage
});

const blogsRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "blogs",
  component: BlogListPage
});

const blogPostRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "blogs/$slug",
  component: BlogPostPage
});

const slicesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "slices",
  component: HomePage
});

const organizationCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "organizations/new",
  component: NewOrganizationPage
});

const sliceCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "slices/new",
  component: SliceCreatePage
});

// Upper bound on how long SSR waits for path-specific prefetches. Anything that
// finishes in time is hydrated; the rest loads client-side as before, so a slow
// API can delay the first byte by at most this much.
const SLICE_PATH_PREFETCH_BUDGET_MS = 1200;

// lookupOrNotFound runs the server render's lookup of the page's own subject
// (the slice, the changeset, the account). When the server says there is no
// such thing and the request has no session, the page is a 404 (a crawler, curl
// or an agent is told so, instead of being sent a page); for anyone who may be
// signed in, the browser decides, as it always did.
async function lookupOrNotFound<T>(lookup: () => Promise<T>): Promise<T> {
  try {
    return await lookup();
  } catch (error) {
    if (isNotFoundError(error)) {
      const { isAnonymousRequest } = await import("../api/serverApi");
      if (await isAnonymousRequest()) {
        throw notFound();
      }
    }
    throw error;
  }
}

const accountRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "accounts/$account",
  loader: async ({ context, params }) => {
    if (import.meta.env.SSR) {
      try {
        const { createServerApiClient } = await import("../api/serverApi");
        const api = await createServerApiClient();
        await lookupOrNotFound(() =>
          context.queryClient.ensureQueryData({
            queryKey: ["accountProfile", params.account],
            queryFn: () => api.getAccountProfile({ account: params.account })
          })
        );
      } catch (error) {
        // Anything else the component finds out for itself.
        if (isNotFound(error)) {
          throw error;
        }
      }
    }
  },
  notFoundComponent: AccountNotFound,
  component: AccountPage
});

const sliceDetailRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "slices/$account/$slice",
  loaderDeps: ({ search }) => ({
    path: pathSearchValue((search as { path?: unknown }).path)
  }),
  loader: async ({ context, deps, params }) => {
    let social: SliceSocial | undefined;
    if (!import.meta.env.SSR && params.account && params.slice) {
      // In the browser the page loads the slice itself; the title follows
      // whatever is already known.
      const known = context.queryClient.getQueryData<Slice>(["sliceRef", params.account, params.slice]);
      if (known) {
        social = sliceSocial(known, params, deps.path, window.location.origin);
      }
    }
    if (import.meta.env.SSR && params.account && params.slice) {
      try {
        const { createServerApiClient, isServerSignedIn } = await import("../api/serverApi");
        const { getRequestUrl } = await import("@tanstack/react-start/server");
        const api = await createServerApiClient();
        const ref = { account: params.account, slice: params.slice };
        // A signed-in viewer's memberships decide what the page offers them
        // (editing, settings). Known in the server render, those parts are in
        // the page from the start instead of appearing after it loads and
        // pushing the README down. Fetched alongside the slice and the ref,
        // so it adds no wait of its own; a failure leaves it to the browser.
        const memberships = isServerSignedIn().then((signedIn) =>
          signedIn
            ? context.queryClient.prefetchQuery({
                queryKey: ["authStatus"],
                queryFn: () => api.getAuthStatus({})
              })
            : undefined
        );
        // The default view resolves the slice and the latest global ref (which
        // yields the commit the file tree renders from); both fire on first
        // paint and have no derived inputs, so prefetch them together.
        const [slice, latest] = await Promise.all([
          lookupOrNotFound(() =>
            context.queryClient.ensureQueryData({
              queryKey: ["sliceRef", params.account, params.slice],
              queryFn: () => api.resolveSlice({ ref })
            })
          ),
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
        await memberships.catch(() => undefined);
        social = sliceSocial(slice, params, deps.path, getRequestUrl().origin);
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
          // A folder's README is part of its page: find it as the page does
          // (through single-folder levels), and ask the server for it
          // rendered, so the page is served with it.
          const readmeFor = async (path: string) => {
            const entries = await listing(path);
            const readme = await findNestedReadme(entries, listing);
            const only = findReadmeEntry(entries) ? undefined : onlyDirectory(entries);
            if (only) {
              context.queryClient.setQueryData(
                nestedReadmeQueryKey(sliceRef, commitId, normalizeRepositoryPath(only.path ?? "")),
                readme ?? null
              );
            }
            if (!readme || Number(readme.size) > README_MAX_BYTES) {
              return;
            }
            const readmePath = normalizeRepositoryPath(readme.path ?? "");
            await context.queryClient.ensureQueryData({
              queryKey: sliceRenderedFileQueryKey(sliceRef, commitId, readmePath),
              queryFn: () => api.renderFile({ commitId, path: readmePath, slice: sliceRef })
            });
          };
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
                    return readmeFor(selectedPath);
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
          } else {
            // A projected folder, or the slice's root.
            work.push(readmeFor(selectedPath));
          }
          await Promise.race([
            Promise.allSettled(work),
            new Promise((resolve) => setTimeout(resolve, SLICE_PATH_PREFETCH_BUDGET_MS))
          ]);
        }
      } catch (error) {
        // A 404 is the answer; any other failure leaves it to the component.
        if (isNotFound(error)) {
          throw error;
        }
      }
    }
    return { social };
  },
  notFoundComponent: SliceNotFound,
  // Link previews read these from the server-rendered page (lib/socialMeta).
  head: ({ loaderData, params }) =>
    loaderData?.social
      ? sliceHead(loaderData.social)
      : { meta: [{ title: `${params.account}/${params.slice} · Gitslice` }] },
  component: SliceDetailPage
});

function sliceSocial(
  slice: Slice,
  params: { account: string; slice: string },
  path: string,
  origin: string
): SliceSocial {
  return {
    account: slice.ref?.account || params.account,
    slice: slice.ref?.slice || params.slice,
    description: slice.description ?? "",
    path,
    origin
  };
}

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
  beforeLoad: ({ params }) => {
    if (!conversationsEnabled) {
      throw redirect({ params: { account: params.account, slice: params.slice }, replace: true, to: "/slices/$account/$slice" });
    }
  },
  component: SliceAgentsPage
});

const sliceAgentConversationRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "slices/$account/$slice/agents/$conversationId",
  beforeLoad: ({ params }) => {
    if (!conversationsEnabled) {
      throw redirect({ params: { account: params.account, slice: params.slice }, replace: true, to: "/slices/$account/$slice" });
    }
  },
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
    slice: (search as { slice?: unknown }).slice,
    status: (search as { status?: unknown }).status,
    q: (search as { q?: unknown }).q
  }),
  loader: async ({ context, deps }) => {
    if (import.meta.env.SSR) {
      const sliceRef = parseSliceSearch(deps.slice);
      if (sliceRef) {
        try {
          const { createServerApiClient } = await import("../api/serverApi");
          const api = await createServerApiClient();
          // The page's first screen: the list as the address asks for it.
          await context.queryClient.prefetchInfiniteQuery(
            changesetsListQuery(
              api,
              sliceRef,
              parseStatusTab(deps.status),
              typeof deps.q === "string" ? deps.q : ""
            )
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
    let social: ChangesetSocial | undefined;
    if (!import.meta.env.SSR) {
      // In the browser the page loads the changeset itself; the title follows
      // whatever is already known.
      const known = context.queryClient.getQueryData<Changeset>(["changeset", params.id]);
      if (known) {
        social = changesetSocial(known, params.id, window.location.origin);
      }
    }
    if (import.meta.env.SSR) {
      try {
        const { createServerApiClient } = await import("../api/serverApi");
        const { getRequestUrl } = await import("@tanstack/react-start/server");
        const api = await createServerApiClient();
        const changeset = await lookupOrNotFound(() =>
          context.queryClient.ensureQueryData({
            queryKey: ["changeset", params.id],
            queryFn: () => api.getChangeset({ changesetId: params.id })
          })
        );
        // Link previews read the server-rendered head (lib/socialMeta).
        social = changesetSocial(changeset, params.id, getRequestUrl().origin);
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
                  api.listChangesets({ authoringSlice, limit: 200, summary: true })
              })
            : Promise.resolve(undefined)
        ]);
      } catch (error) {
        // A 404 is the answer; any other failure leaves it to the component.
        if (isNotFound(error)) {
          throw error;
        }
      }
    }
    return { social };
  },
  notFoundComponent: ChangesetNotFound,
  // The page's title and what a shared link shows come from the changeset.
  // Without one (not found, or not readable by this viewer) the head says
  // nothing about it.
  head: ({ loaderData, params }) =>
    loaderData?.social
      ? changesetHead(loaderData.social)
      : { meta: [{ title: `Changeset ${params.id} · Gitslice` }] },
  component: ChangesetDetailPage
});

function changesetSocial(changeset: Changeset, requestedId: string, origin: string): ChangesetSocial {
  return {
    handle: shortChangesetId(changeset.id ?? "") || requestedId,
    title: changeset.title ?? "",
    description: changeset.description ?? "",
    status: changeset.status ?? "",
    author: changeset.author ?? "",
    account: changeset.authoringSlice?.account ?? "",
    slice: changeset.authoringSlice?.slice ?? "",
    fileCount: changeset.affectedPaths?.length ?? 0,
    origin
  };
}

const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
  loginFlowRoute,
  signupRoute,
  signupFlowRoute,
  cliLoginRoute,
  appRoute.addChildren([
    claimsRoute,
    conversationsRoute,
    slicesRoute,
    sliceCreateRoute,
    organizationCreateRoute,
    sliceSettingsRoute
  ]),
  publicAppRoute.addChildren([
    docRoute,
    docSectionRoute,
    blogsRoute,
    blogPostRoute,
    accountRoute,
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
    // A route with no page of its own for a missing thing, and an address that
    // matches no route, get this.
    defaultNotFoundComponent: PageNotFound,
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
