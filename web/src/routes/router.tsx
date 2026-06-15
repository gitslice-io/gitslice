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
import { DocPage } from "./DocPage";
import { HomePage } from "./HomePage";
import { LoginPage } from "./LoginPage";
import { SliceCreatePage } from "./SliceCreatePage";
import { SliceDetailPage } from "./SliceDetailPage";
import { SliceSettingsPage } from "./SliceSettingsPage";
import { SlicesPage } from "./SlicesPage";

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
    docRoute,
    docSectionRoute,
    slicesRoute,
    sliceCreateRoute,
    sliceDetailRoute,
    sliceSettingsRoute,
    changesetsRoute,
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
