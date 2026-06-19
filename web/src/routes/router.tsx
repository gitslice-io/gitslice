import {
  Outlet,
  createRootRoute,
  createRoute,
  createRouter
} from "@tanstack/react-router";
import type { ReactNode } from "react";

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

const rootRoute = createRootRoute({
  component: () => <Outlet />
});

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
  getParentRoute: () => appRoute,
  path: "slices/$id",
  component: SliceDetailPage
});

const sliceSettingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "slices/$id/settings",
  component: SliceSettingsPage
});

const changesetsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "changesets",
  component: ChangesetsPage
});

const stacksRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "dependencies",
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
  getParentRoute: () => appRoute,
  path: "cs/$id",
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
    sliceDetailRoute,
    sliceSettingsRoute,
    changesetsRoute,
    stacksRoute,
    stackCreateRoute,
    stackDetailRoute,
    stackRestackRoute,
    stackSubmitRoute,
    changesetShortRoute
  ])
]);

export const router = createRouter({
  routeTree,
  defaultPreload: "intent",
  scrollRestoration: true
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
