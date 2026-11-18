import { createRoute, notFound, useRouterState } from "@tanstack/react-router";

import { isNotFoundError } from "../api/rpcError";
import { RequireAuth } from "../auth/RequireAuth";
import { AppShell } from "../components/AppShell";
import { VisitorBanner } from "../components/VisitorBanner";
import { visitorBannerWanted } from "../lib/visitorBanner";
import { authStatusQuery } from "../queries/auth";
import { SelectionProvider } from "../state/selection";
import { UsernameGate } from "./UsernameGate";
import { rootRoute } from "./root";

// lookupOrNotFound runs the server render's lookup of the page's own subject
// (the slice, the changeset, the account). When the server says there is no
// such thing and the request has no session, the page is a 404 (a crawler, curl
// or an agent is told so, instead of being sent a page); for anyone who may be
// signed in, the browser decides, as it always did.
export async function lookupOrNotFound<T>(lookup: () => Promise<T>): Promise<T> {
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

export const appRoute = createRoute({
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

export const publicAppRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "publicApp",
  // Whether to offer a visitor with no session an introduction: from the
  // cookies, which the server render and the browser both see.
  loader: async () => {
    if (import.meta.env.SSR) {
      const { getRequestHeader } = await import("@tanstack/react-start/server");
      return { visitorBanner: visitorBannerWanted(getRequestHeader("cookie") ?? "") };
    }
    return { visitorBanner: visitorBannerWanted(document.cookie) };
  },
  component: PublicAppLayout
});

function PublicAppLayout(): JSX.Element {
  const loaded = useRouterState({
    select: (state) => state.matches.find((match) => match.routeId === publicAppRoute.id)?.loaderData
  }) as { visitorBanner?: boolean } | undefined;
  return (
    <SelectionProvider>
      <AppShell banner={<VisitorBanner show={loaded?.visitorBanner ?? false} />} />
    </SelectionProvider>
  );
}
