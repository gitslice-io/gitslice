import {
  createRoute,
  createRouter,
  isNotFound,
  lazyRouteComponent,
  redirect
} from "@tanstack/react-router";
import { dehydrate, type DehydratedState } from "@tanstack/react-query";

import { conversationsEnabled } from "../lib/features";
import { changesetHead, sliceHead } from "../lib/socialMeta";
import { getPostMeta } from "./blog/postIndex";
import { accountProfileQuery } from "../queries/accounts";
import { conversationQuery } from "../queries/agents";
import { authStatusQuery } from "../queries/auth";
import { changesetsListQuery } from "../queries/changesets";
import { globalRefQuery, sliceRefQuery } from "../queries/slices";
import { parseStatusTab } from "../lib/changesetFilters";
import { appRoute, lookupOrNotFound, publicAppRoute } from "./layouts";
import { loadChangesetDetail } from "./loaders/changesetDetailLoader";
import { loadSliceDetail } from "./loaders/sliceDetailLoader";
import { AccountNotFound, ChangesetNotFound, PageNotFound, SliceNotFound } from "./NotFoundPage";
import { createQueryClient, rootRoute, stripUndefined } from "./root";
import {
  validateAccountSearch,
  validateChangesetDetailSearch,
  validateChangesetsSearch,
  validateSliceCreateSearch,
  validateSliceDetailSearch
} from "./searchSchemas";
import { parseSliceSearch } from "./stackPageUtils";

// Each page's code is fetched when its route is opened, not with every page:
// a visitor reading a slice does not download the docs, the blog, the diff
// viewer or the settings forms.
const lazyPages = {
  AboutPage: lazyRouteComponent(() => import("./AboutPage"), "AboutPage"),
  AccountPage: lazyRouteComponent(() => import("./AccountPage"), "AccountPage"),
  AccountSecretsPage: lazyRouteComponent(() => import("./AccountSecretsPage"), "AccountSecretsPage"),
  AgentLoginPage: lazyRouteComponent(() => import("./AgentLoginPage"), "AgentLoginPage"),
  BlogListPage: lazyRouteComponent(() => import("./BlogListPage"), "BlogListPage"),
  BlogPostPage: lazyRouteComponent(() => import("./BlogPostPage"), "BlogPostPage"),
  ChangelogPage: lazyRouteComponent(() => import("./ChangelogPage"), "ChangelogPage"),
  ChangesetDetailPage: lazyRouteComponent(() => import("./ChangesetDetailPage"), "ChangesetDetailPage"),
  ChangesetsPage: lazyRouteComponent(() => import("./ChangesetsPage"), "ChangesetsPage"),
  ClaimsPage: lazyRouteComponent(() => import("./ClaimsPage"), "ClaimsPage"),
  CliLoginPage: lazyRouteComponent(() => import("./CliLoginPage"), "CliLoginPage"),
  ConversationsPage: lazyRouteComponent(() => import("./ConversationsPage"), "ConversationsPage"),
  DocPage: lazyRouteComponent(() => import("./DocPage"), "DocPage"),
  HomePage: lazyRouteComponent(() => import("./HomePage"), "HomePage"),
  IndexPage: lazyRouteComponent(() => import("./IndexPage"), "IndexPage"),
  LoginPage: lazyRouteComponent(() => import("./LoginPage"), "LoginPage"),
  NewOrganizationPage: lazyRouteComponent(() => import("./NewOrganizationPage"), "NewOrganizationPage"),
  PricingPage: lazyRouteComponent(() => import("./PricingPage"), "PricingPage"),
  PrivacyPage: lazyRouteComponent(() => import("./PrivacyPage"), "PrivacyPage"),
  SecurityPage: lazyRouteComponent(() => import("./SecurityPage"), "SecurityPage"),
  SignupPage: lazyRouteComponent(() => import("./SignupPage"), "SignupPage"),
  SliceAgentsPage: lazyRouteComponent(() => import("./SliceAgentsPage"), "SliceAgentsPage"),
  SliceCreatePage: lazyRouteComponent(() => import("./SliceCreatePage"), "SliceCreatePage"),
  SliceDetailPage: lazyRouteComponent(() => import("./SliceDetailPage"), "SliceDetailPage"),
  SliceSettingsPage: lazyRouteComponent(() => import("./SliceSettingsPage"), "SliceSettingsPage"),
  StatusPage: lazyRouteComponent(() => import("./StatusPage"), "StatusPage"),
  TermsPage: lazyRouteComponent(() => import("./TermsPage"), "TermsPage")
};

const loginRoute = createRoute({ getParentRoute: () => rootRoute, path: "/login", component: lazyPages.LoginPage });
const loginFlowRoute = createRoute({ getParentRoute: () => rootRoute, path: "/login/$", component: lazyPages.LoginPage });
const agentLoginRoute = createRoute({ getParentRoute: () => rootRoute, path: "/agent-login", component: lazyPages.AgentLoginPage });
const signupRoute = createRoute({ getParentRoute: () => rootRoute, path: "/signup", component: lazyPages.SignupPage });
const signupFlowRoute = createRoute({ getParentRoute: () => rootRoute, path: "/signup/$", component: lazyPages.SignupPage });
const cliLoginRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cli-login", component: lazyPages.CliLoginPage });

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  loader: async ({ context }) => {
    if (!import.meta.env.SSR) return;
    try {
      const { createServerApiClient, isServerSignedIn } = await import("../api/serverApi");
      if (!(await isServerSignedIn())) return;
      await context.queryClient.ensureQueryData(authStatusQuery(await createServerApiClient()));
    } catch {
      // The component keeps the existing client-side load/error behavior.
    }
  },
  component: lazyPages.IndexPage
});

const claimsRoute = createRoute({ getParentRoute: () => appRoute, path: "claims", component: lazyPages.ClaimsPage });
const conversationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "conversations",
  beforeLoad: () => {
    if (!conversationsEnabled) throw redirect({ replace: true, to: "/" });
  },
  component: lazyPages.ConversationsPage
});

const docRoute = createRoute({ getParentRoute: () => publicAppRoute, path: "doc", component: lazyPages.DocPage });
const docSectionRoute = createRoute({ getParentRoute: () => publicAppRoute, path: "doc/$section", component: lazyPages.DocPage });
const blogsRoute = createRoute({ getParentRoute: () => publicAppRoute, path: "blogs", component: lazyPages.BlogListPage });
const securityRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "security",
  head: () => staticPageHead("Security", "How Gitslice protects source code, encrypted folder secrets, CI runners, and agent workflows.", "/security"),
  component: lazyPages.SecurityPage
});
const pricingRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "pricing",
  head: () => staticPageHead("Pricing", "Transparent plans for individuals, teams, open source, and self-hosted deployments.", "/pricing"),
  component: lazyPages.PricingPage
});
const changelogRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "changelog",
  head: () => staticPageHead("Changelog", "Recent releases, performance improvements, and platform updates shipped on Gitslice.", "/changelog"),
  component: lazyPages.ChangelogPage
});
const statusRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "status",
  head: () => staticPageHead("Status", "Live health check and service status for Gitslice.", "/status"),
  component: lazyPages.StatusPage
});
const aboutRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "about",
  head: () => staticPageHead("About", "About Gitslice — Git-compatible source infrastructure built for parallel teams and agents.", "/about"),
  component: lazyPages.AboutPage
});
const privacyRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "privacy",
  head: () => staticPageHead("Privacy Policy", "Gitslice Privacy Policy.", "/privacy"),
  component: lazyPages.PrivacyPage
});
const termsRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "terms",
  head: () => staticPageHead("Terms of Service", "Gitslice Terms of Service.", "/terms"),
  component: lazyPages.TermsPage
});
const blogPostRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "blogs/$slug",
  // The post's Markdown is loaded before render, so the server sends the
  // whole article (each body is its own code-split chunk).
  loader: async ({ params }) => {
    const { loadPostMarkdown } = await import("./blog/postIndex");
    return { markdown: (await loadPostMarkdown(params.slug)) ?? null };
  },
  head: ({ params }) => blogPostHead(params.slug),
  component: lazyPages.BlogPostPage
});
const slicesRoute = createRoute({ getParentRoute: () => appRoute, path: "slices", component: lazyPages.HomePage });
const organizationCreateRoute = createRoute({ getParentRoute: () => appRoute, path: "organizations/new", component: lazyPages.NewOrganizationPage });
const sliceCreateRoute = createRoute({ getParentRoute: () => appRoute, path: "slices/new", validateSearch: validateSliceCreateSearch, component: lazyPages.SliceCreatePage });
const accountSecretsRoute = createRoute({ getParentRoute: () => appRoute, path: "accounts/$account/secrets", component: lazyPages.AccountSecretsPage });

const accountRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "accounts/$account",
  validateSearch: validateAccountSearch,
  loader: async ({ context, params }) => {
    if (!import.meta.env.SSR) return;
    try {
      const { createServerApiClient } = await import("../api/serverApi");
      const api = await createServerApiClient();
      await lookupOrNotFound(() => context.queryClient.ensureQueryData(accountProfileQuery(api, params.account)));
    } catch (error) {
      if (isNotFound(error)) throw error;
    }
  },
  notFoundComponent: AccountNotFound,
  component: lazyPages.AccountPage
});

const sliceDetailRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "slices/$account/$slice",
  validateSearch: validateSliceDetailSearch,
  loaderDeps: ({ search }) => ({ path: search.path ?? "", at: search.at ?? "" }),
  loader: loadSliceDetail,
  notFoundComponent: SliceNotFound,
  head: ({ loaderData, params }) =>
    loaderData?.social ? sliceHead(loaderData.social) : { meta: [{ title: `${params.account}/${params.slice} · Gitslice` }] },
  component: lazyPages.SliceDetailPage
});

const sliceSettingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "slices/$account/$slice/settings",
  loader: async ({ context, params }) => {
    if (!import.meta.env.SSR || !params.account || !params.slice) return;
    try {
      const { createServerApiClient } = await import("../api/serverApi");
      const api = await createServerApiClient();
      await Promise.all([
        context.queryClient.ensureQueryData(sliceRefQuery(api, params.account, params.slice)),
        context.queryClient.ensureQueryData(globalRefQuery(api))
      ]);
    } catch {
      // The component keeps the existing client-side load/error behavior.
    }
  },
  component: lazyPages.SliceSettingsPage
});

const sliceAgentsRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "slices/$account/$slice/agents",
  beforeLoad: ({ params }) => {
    if (!conversationsEnabled) throw redirect({ params: { account: params.account, slice: params.slice }, replace: true, to: "/slices/$account/$slice" });
  },
  component: lazyPages.SliceAgentsPage
});

const sliceAgentConversationRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "slices/$account/$slice/agents/$conversationId",
  beforeLoad: ({ params }) => {
    if (!conversationsEnabled) throw redirect({ params: { account: params.account, slice: params.slice }, replace: true, to: "/slices/$account/$slice" });
  },
  loader: async ({ context, params }) => {
    if (!import.meta.env.SSR || !params.conversationId) return;
    try {
      const { createServerApiClient } = await import("../api/serverApi");
      await context.queryClient.ensureQueryData(conversationQuery(await createServerApiClient(), params.conversationId));
    } catch {
      // The component keeps the existing client-side load/error behavior.
    }
  },
  component: lazyPages.SliceAgentsPage
});

const changesetsRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "changesets",
  validateSearch: validateChangesetsSearch,
  loaderDeps: ({ search }) => ({ slice: search.slice, status: search.status, q: search.q }),
  loader: async ({ context, deps }) => {
    if (!import.meta.env.SSR) return;
    const sliceRef = parseSliceSearch(deps.slice);
    if (!sliceRef) return;
    try {
      const { createServerApiClient } = await import("../api/serverApi");
      await context.queryClient.prefetchInfiniteQuery(changesetsListQuery(await createServerApiClient(), sliceRef, parseStatusTab(deps.status), deps.q ?? ""));
    } catch {
      // The component keeps the existing client-side load/error behavior.
    }
  },
  component: lazyPages.ChangesetsPage
});

const changesetShortRoute = createRoute({
  getParentRoute: () => publicAppRoute,
  path: "cs/$id",
  validateSearch: validateChangesetDetailSearch,
  loaderDeps: ({ search }) => ({ from: search.from, to: search.to }),
  loader: loadChangesetDetail,
  notFoundComponent: ChangesetNotFound,
  head: ({ loaderData, params }) =>
    loaderData?.social ? changesetHead(loaderData.social) : { meta: [{ title: `Changeset ${params.id} · Gitslice` }] },
  component: lazyPages.ChangesetDetailPage
});

function staticPageHead(title: string, description: string, path: string) {
  const url = `https://gitslice.io${path}`;
  return {
    meta: [
      { title: `${title} · Gitslice` },
      { name: "description", content: description },
      { property: "og:title", content: `${title} · Gitslice` },
      { property: "og:description", content: description },
      { property: "og:url", content: url }
    ],
    links: [{ rel: "canonical", href: url }]
  };
}

function blogPostHead(slug: string) {
  const post = getPostMeta(slug);
  if (!post) {
    return { meta: [{ title: "Blog · Gitslice" }] };
  }
  const url = `https://gitslice.io/blogs/${post.slug}`;
  return {
    meta: [
      { title: `${post.title} · Gitslice` },
      { name: "description", content: post.dek },
      { property: "og:type", content: "article" },
      { property: "og:title", content: post.title },
      { property: "og:description", content: post.dek },
      { property: "og:url", content: url },
      { property: "article:published_time", content: post.date },
      { name: "twitter:card", content: "summary" },
      { name: "twitter:title", content: post.title },
      { name: "twitter:description", content: post.dek }
    ],
    links: [{ rel: "canonical", href: url }]
  };
}

const routeTree = rootRoute.addChildren([
  indexRoute, loginRoute, loginFlowRoute, agentLoginRoute, signupRoute, signupFlowRoute, cliLoginRoute,
  appRoute.addChildren([claimsRoute, conversationsRoute, slicesRoute, sliceCreateRoute, organizationCreateRoute, sliceSettingsRoute, accountSecretsRoute]),
  publicAppRoute.addChildren([
    docRoute, docSectionRoute, blogsRoute, blogPostRoute,
    securityRoute, pricingRoute, changelogRoute, statusRoute, aboutRoute, privacyRoute, termsRoute,
    accountRoute, sliceDetailRoute, sliceAgentsRoute, sliceAgentConversationRoute, changesetsRoute, changesetShortRoute
  ])
]);

export function getRouter() {
  const queryClient = createQueryClient();
  let dehydratedQueryState: DehydratedState | undefined;
  return createRouter({
    routeTree,
    context: { getDehydratedQueryState: () => dehydratedQueryState, queryClient },
    defaultNotFoundComponent: PageNotFound,
    defaultPreload: "intent",
    defaultPreloadStaleTime: 0,
    scrollRestoration: true,
    dehydrate: () => {
      dehydratedQueryState = dehydrate(queryClient, { shouldDehydrateMutation: () => false });
      return { queryClient: stripUndefined(dehydratedQueryState) as never };
    },
    hydrate: (dehydrated) => {
      dehydratedQueryState = dehydrated?.queryClient as DehydratedState | undefined;
    }
  });
}

declare module "@tanstack/react-router" {
  interface Register { router: ReturnType<typeof getRouter>; }
}

declare module "@tanstack/react-start" {
  interface Register { ssr: true; router: ReturnType<typeof getRouter>; }
}
