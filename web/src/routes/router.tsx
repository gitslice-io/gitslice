import {
  Outlet,
  createRootRoute,
  createRoute,
  createRouter
} from "@tanstack/react-router";

import { RequireAuth } from "../auth/RequireAuth";
import { AppShell } from "../components/AppShell";
import { SelectionProvider } from "../state/selection";
import { ChangesetDetailPage } from "./ChangesetDetailPage";
import { ChangesetsPage } from "./ChangesetsPage";
import { CliLoginPage } from "./CliLoginPage";
import { HomePage } from "./HomePage";
import { LoginPage } from "./LoginPage";
import { NewChangesetPage } from "./NewChangesetPage";
import { SliceDetailPage } from "./SliceDetailPage";
import { SliceSettingsPage } from "./SliceSettingsPage";
import { SlicesPage } from "./SlicesPage";
import { SourcePage } from "./SourcePage";

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
        <AppShell />
      </SelectionProvider>
    </RequireAuth>
  )
});

const homeRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  component: HomePage
});

const sourceRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "source/$account",
  component: SourcePage
});

const sourceSplatRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "source/$account/$",
  component: SourcePage
});

const slicesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "slices",
  component: SlicesPage
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

const newChangesetRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "changesets/new",
  component: NewChangesetPage
});

const changesetDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "changesets/$id",
  component: ChangesetDetailPage
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  cliLoginRoute,
  appRoute.addChildren([
    homeRoute,
    sourceRoute,
    sourceSplatRoute,
    slicesRoute,
    sliceDetailRoute,
    sliceSettingsRoute,
    changesetsRoute,
    newChangesetRoute,
    changesetDetailRoute
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
